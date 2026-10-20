"""Model-agnostic ONNX sentiment inference, without torch at runtime.

The session and the tokenizer are **injected**, not loaded here: only
`from_model_dir` touches the filesystem. That keeps this module testable without
a 100 MB weight file, and keeps every model-specific decision (which file, which
providers, which label order) in one classmethod.

Four rules cost real bugs if broken:

1. **Tensor names come from the session** (`get_inputs()`), never from a
   hardcoded list. Some exports have `token_type_ids`, some do not; assuming it
   is there turns a working model into a `RuntimeException` at the first call.
2. **Padding is computed per batch and applied to the arrays here**, not through
   the tokenizer's own padding, whose `enable_padding(length=...)` persists.
   Reusing batch 1's width for batch 2 means paying for 40 columns to score a
   3-token message, on every later batch. See `workmem/eval/bakeoff.py` for the
   version of this that does leak.
3. **Blank text is not a prediction.** It returns `("neutral", 0.0)` and never
   reaches the session, which is the same contract the dictionary path has: no
   opinion must stay distinguishable from a confident middle.
4. **The neutral band is a strict inequality** (`|p - 0.5| < tau`). On the
   boundary the model still states an opinion; `<=` would swallow exactly the
   borderline cases a band sweep is supposed to measure.

`predict` deliberately does NOT apply the band: which tau to use is decided on an
evaluation set (Task 0), not by the inference wrapper, so `apply_neutral_band`
stays a separate, pure function of probabilities.

Importing this module is cheap on purpose: numpy only. onnxruntime and
tokenizers are imported inside `from_model_dir`.
"""

from __future__ import annotations

import json
import pathlib

import numpy as np

# Both "no opinion" markers in one place: callers compare against it.
NEUTRAL_LABEL = "neutral"
NEUTRAL_CONFIDENCE = 0.0

POSITIVE_LABEL = "positive"
NEGATIVE_LABEL = "negative"


def labels_from_config(config: dict) -> list[str]:
    """Read `id2label` as a list ordered by id 0..n-1.

    By id, not by key order and not by value: index 0 is whatever the export says
    it is. `sorted(id2label.values())` happens to work for one model and
    silently swaps Positive/Negative for the next one, which flips the sign of
    every score in the report.
    """
    id2label = config["id2label"]
    return [id2label[str(index)] for index in range(len(id2label))]


class Encoder:
    """A tokenizer plus an ONNX classifier, producing per-text probabilities.

    `session` is an `onnxruntime.InferenceSession`, `tokenizer` a
    `tokenizers.Tokenizer`; both are injected so that tests need no model file.
    """

    def __init__(self, session, tokenizer, labels: list[str], max_length: int = 128):
        if not labels:
            raise ValueError("labels must not be empty: an unnamed class cannot be reported")
        self.session = session
        self.tokenizer = tokenizer
        self.labels = list(labels)
        self.max_length = max_length
        self.input_names = [spec.name for spec in session.get_inputs()]
        # Long messages are cut here rather than at the model: a 128-token cap
        # bounds the cost of one absurd row without affecting ordinary chat.
        tokenizer.enable_truncation(max_length=max_length)

    @classmethod
    def from_model_dir(cls, directory, model_file: str = "model_quantized.onnx") -> "Encoder":
        """Build an encoder from a downloaded model directory.

        `providers=["CPUExecutionProvider"]` is not a hint but a pin: the service
        ships without CUDA, and letting onnxruntime pick can try to initialise a
        GPU and fail on a machine that has one half-installed.
        """
        from onnxruntime import InferenceSession
        from tokenizers import Tokenizer

        directory = pathlib.Path(directory)
        config = json.loads((directory / "config.json").read_text(encoding="utf-8"))
        tokenizer = Tokenizer.from_file(str(directory / "tokenizer.json"))
        session = InferenceSession(str(directory / model_file), providers=["CPUExecutionProvider"])
        return cls(session=session, tokenizer=tokenizer, labels=labels_from_config(config))

    def probabilities(self, texts: list[str]) -> list[list[float]]:
        """Softmax probabilities for each text, one row per input, in order.

        Callers that may hold blank strings should use `predict`; this method
        encodes exactly what it is given.
        """
        if not texts:
            return []

        encodings = self.tokenizer.encode_batch(list(texts))
        width = max(len(encoding.ids) for encoding in encodings)

        input_ids = np.zeros((len(encodings), width), dtype=np.int64)
        attention_mask = np.zeros((len(encodings), width), dtype=np.int64)
        token_type_ids = np.zeros((len(encodings), width), dtype=np.int64)
        for row, encoding in enumerate(encodings):
            length = len(encoding.ids)
            input_ids[row, :length] = encoding.ids
            attention_mask[row, :length] = encoding.attention_mask
            token_type_ids[row, :length] = encoding.type_ids

        feeds = {"input_ids": input_ids}
        if "attention_mask" in self.input_names:
            feeds["attention_mask"] = attention_mask
        if "token_type_ids" in self.input_names:
            feeds["token_type_ids"] = token_type_ids

        # float64 throughout: the sum below must land within 1e-6 of 1 even for
        # the extreme logits a bad input can produce.
        logits = np.asarray(self.session.run(None, feeds)[0], dtype=np.float64)
        shifted = logits - logits.max(axis=1, keepdims=True)
        weights = np.exp(shifted)
        return (weights / weights.sum(axis=1, keepdims=True)).tolist()

    def predict(self, texts: list[str]) -> list[tuple[str, float]]:
        """`(label, confidence)` per text, in input order, blanks included.

        Blank or whitespace-only text returns `("neutral", 0.0)` and is filtered
        out of the batch instead of being encoded: a model asked to score "" will
        happily answer, and that answer is noise.

        No neutral band is applied here — the label is the model's own, as named
        by `config.json` (e.g. `Positive`). See `apply_neutral_band`.
        """
        if not texts:
            return []

        results: list[tuple[str, float]] = [(NEUTRAL_LABEL, NEUTRAL_CONFIDENCE)] * len(texts)
        kept = [index for index, text in enumerate(texts) if text and text.strip()]
        if not kept:
            return results

        for row, index in zip(self.probabilities([texts[i] for i in kept]), kept):
            best = int(np.argmax(row))
            if best >= len(self.labels):
                raise ValueError(
                    f"model produced {len(row)} classes but {len(self.labels)} labels are configured"
                )
            results[index] = (self.labels[best], float(row[best]))
        return results


def apply_neutral_band(probs: list[list[float]], tau: float, positive_index: int) -> list[str]:
    """Turn per-text probabilities into `positive` / `negative` / `neutral`.

    A binary model gives every sentence a polarity — measured, the model called
    "明天几点" positive with 0.76. So `|p - 0.5| < tau` is recorded as neutral,
    and the boundary itself still counts as an opinion: tau is the width of the
    "no opinion" region, not a threshold to round.

    `positive_index` says which column holds P(positive); it differs per export
    and is read from the config's label order.
    """
    labels: list[str] = []
    for row in probs:
        positive = row[positive_index]
        if abs(positive - 0.5) < tau:
            labels.append(NEUTRAL_LABEL)
        elif positive > 0.5:
            labels.append(POSITIVE_LABEL)
        else:
            labels.append(NEGATIVE_LABEL)
    return labels
