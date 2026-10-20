"""M2 Task 1 RED: model-agnostic ONNX sentiment encoder.

Contract pinned here:

- `Encoder` takes an *injected* session and tokenizer, so nothing in this file
  needs a 100 MB model: the doubles below mirror the slice of the real APIs the
  encoder may touch (`get_inputs` / `run` and `enable_truncation` /
  `encode_batch`).
- Blank texts come back as `("neutral", 0.0)` and are NEVER fed to the model,
  which is also what the dictionary path did: no opinion is not a prediction.
- One result per input, in input order, blanks included: Go addresses results by
  `msg_id`, so a dropped element would misalign every score after it.
- The label vocabulary comes from `config.json`'s `id2label` **by id 0..n-1**,
  never from sorting: index 0 is whatever the export says it is.
- Padding is computed per batch from that batch's longest text. A persistent
  `enable_padding(length=...)` (see `workmem/eval/bakeoff.py`) makes every later
  batch pay for the first long batch; this encoder pads the arrays itself.
- Input names are read from the session, so an export without `token_type_ids`
  still runs.
- `apply_neutral_band` uses `|p - 0.5| < tau` **strictly**: on the boundary the
  model still states an opinion. `<=` would quietly eat the confident-but-faint
  cases the band exists to measure.
"""

from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import types

import numpy as np
import pytest
import tokenizers

from app.onnx_encoder import Encoder, apply_neutral_band, labels_from_config

SERVICE_DIR = pathlib.Path(__file__).resolve().parents[1]

# id2label in a deliberately scrambled insertion order, with values that are
# neither in id order nor in sorted order. Only "read ids 0,1,2" gives the
# expected answer, so a `values()` or `sorted(values())` implementation fails.
SCRAMBLED_ID2LABEL = {"2": "Neutral", "1": "Negative", "0": "Positive"}
EXPECTED_LABELS = ["Positive", "Negative", "Neutral"]


class FakeTokenizer:
    """Stand-in for `tokenizers.Tokenizer`: one id per character, no vocab file.

    Implements only what the encoder is allowed to call. `encode_batch` returns
    variable-length encodings on purpose — that is exactly the shape a real
    tokenizer gives before padding, and the encoder has to pad it correctly.
    """

    def __init__(self) -> None:
        self.truncation: int | None = None
        self.encode_calls: list[list[str]] = []

    def enable_truncation(self, max_length: int) -> None:
        self.truncation = max_length

    def encode_batch(self, texts: list[str]) -> list[types.SimpleNamespace]:
        self.encode_calls.append(list(texts))
        encodings = []
        for text in texts:
            ids = [1 + (ord(char) % 50) for char in text]
            if self.truncation is not None:
                ids = ids[: self.truncation]
            encodings.append(
                types.SimpleNamespace(
                    ids=ids,
                    attention_mask=[1] * len(ids),
                    type_ids=[0] * len(ids),
                )
            )
        return encodings


class FakeSession:
    """Stand-in for `onnxruntime.InferenceSession` that counts its own calls."""

    def __init__(self, input_names: tuple[str, ...] = ("input_ids", "attention_mask"), logits_for=None):
        self.input_names = list(input_names)
        self._logits_for = logits_for or (lambda rows: [[0.0, 0.0] for _ in range(rows)])
        self.run_count = 0
        self.feeds: list[dict[str, np.ndarray]] = []

    def get_inputs(self) -> list[types.SimpleNamespace]:
        return [types.SimpleNamespace(name=name) for name in self.input_names]

    def run(self, output_names, feeds):
        self.run_count += 1
        self.feeds.append({key: np.array(value, copy=True) for key, value in feeds.items()})
        return [np.asarray(self._logits_for(int(feeds["input_ids"].shape[0])), dtype=np.float32)]


def make_encoder(session: FakeSession, tokenizer: FakeTokenizer | None = None, labels=None) -> Encoder:
    return Encoder(
        session=session,
        tokenizer=tokenizer or FakeTokenizer(),
        labels=labels if labels is not None else EXPECTED_LABELS,
    )


def test_predict_of_empty_batch_is_empty_and_touches_nothing():
    session = FakeSession()
    tokenizer = FakeTokenizer()
    encoder = make_encoder(session, tokenizer)

    assert encoder.predict([]) == []
    assert session.run_count == 0
    assert tokenizer.encode_calls == []


def test_blank_texts_are_neutral_and_never_reach_the_model():
    """Whitespace is "no opinion", not a prediction: no run, no encoding."""
    session = FakeSession()
    tokenizer = FakeTokenizer()
    encoder = make_encoder(session, tokenizer)

    assert encoder.predict(["", "   ", "\t\n "]) == [("neutral", 0.0), ("neutral", 0.0), ("neutral", 0.0)]
    assert session.run_count == 0
    assert tokenizer.encode_calls == []


def test_result_count_matches_input_and_blanks_keep_their_position():
    session = FakeSession(logits_for=lambda rows: [[1.0, 3.0] for _ in range(rows)])
    tokenizer = FakeTokenizer()
    encoder = make_encoder(session, tokenizer, labels=["Negative", "Positive"])

    results = encoder.predict(["happy", "", "   ", "sad"])

    assert len(results) == 4
    assert results[1] == ("neutral", 0.0)
    assert results[2] == ("neutral", 0.0)
    assert results[0][0] == "Positive"
    assert results[3][0] == "Positive"
    # The blanks were filtered before encoding, so the batch is the two real texts.
    assert tokenizer.encode_calls == [["happy", "sad"]]


def test_labels_from_config_follow_id_order_not_alphabetical_or_insertion_order():
    assert labels_from_config({"id2label": SCRAMBLED_ID2LABEL}) == EXPECTED_LABELS


def test_predict_takes_its_label_from_the_config_order():
    """argmax 1 must name `Negative` because id 1 is Negative, not by luck."""
    session = FakeSession(logits_for=lambda rows: [[0.0, 5.0] for _ in range(rows)])
    encoder = make_encoder(session, labels=["Positive", "Negative"])

    assert [label for label, _ in encoder.predict(["a", "b"])] == ["Negative", "Negative"]


def test_confidence_is_the_softmax_probability_of_the_chosen_label():
    session = FakeSession(logits_for=lambda rows: [[0.0, 5.0] for _ in range(rows)])
    encoder = make_encoder(session, labels=["Positive", "Negative"])

    label, confidence = encoder.predict(["a"])[0]

    assert label == "Negative"
    assert confidence == pytest.approx(1.0 / (1.0 + np.exp(-5.0)), abs=1e-6)
    assert 0.0 <= confidence <= 1.0


def test_probabilities_are_softmax_normalised_and_do_not_overflow():
    """logits of ±1000 must not become inf/inf: subtract the row max first."""
    session = FakeSession(logits_for=lambda rows: [[1000.0, -1000.0] for _ in range(rows)])
    encoder = make_encoder(session, labels=["Positive", "Negative"])

    probs = encoder.probabilities(["a", "bc"])

    assert len(probs) == 2
    for row in probs:
        assert len(row) == 2
        assert sum(row) == pytest.approx(1.0, abs=1e-6)
        assert row[0] == pytest.approx(1.0, abs=1e-6)


def test_padding_follows_the_current_batch_and_never_leaks():
    """The regression `bakeoff.py` carries: a persistent padding width.

    Batch 1 is 40 tokens wide. Batch 2 is two texts of 2 and 3 tokens. If the
    encoder reuses batch 1's width, batch 2 costs 40 columns of useless
    compute — and on a real export, real memory.
    """
    session = FakeSession()
    encoder = make_encoder(session)

    encoder.probabilities(["a" * 40, "b" * 33])
    assert session.feeds[0]["input_ids"].shape == (2, 40)

    encoder.probabilities(["ab", "abc"])

    ids = session.feeds[1]["input_ids"]
    mask = session.feeds[1]["attention_mask"]
    assert ids.shape == (2, 3), "batch 2 must be padded to its own longest text"
    assert mask.shape == (2, 3)
    # And the padding must be masked out, not just narrow.
    assert mask.tolist() == [[1, 1, 0], [1, 1, 1]]
    assert ids[0].tolist()[2] == 0


def test_session_without_token_type_ids_is_fed_without_that_tensor():
    """Some exports have no `token_type_ids`; hardcoding it would crash."""
    session = FakeSession(input_names=("input_ids", "attention_mask"))
    encoder = make_encoder(session)

    assert encoder.predict(["a"])[0][0] in EXPECTED_LABELS

    assert session.run_count == 1
    assert set(session.feeds[0]) == {"input_ids", "attention_mask"}


def test_session_with_token_type_ids_gets_them():
    session = FakeSession(input_names=("input_ids", "attention_mask", "token_type_ids"))
    encoder = make_encoder(session)

    encoder.predict(["ab"])

    assert set(session.feeds[0]) == {"input_ids", "attention_mask", "token_type_ids"}
    assert session.feeds[0]["token_type_ids"].tolist() == [[0, 0]]


def test_long_text_is_truncated_to_max_length():
    session = FakeSession()
    encoder = Encoder(session=session, tokenizer=FakeTokenizer(), labels=EXPECTED_LABELS, max_length=8)

    encoder.probabilities(["a" * 50])

    assert session.feeds[0]["input_ids"].shape == (1, 8)


def test_from_model_dir_reads_labels_by_id_and_pins_the_cpu_provider(tmp_path, monkeypatch):
    directory = tmp_path / "erlangshen-110m-sentiment"
    directory.mkdir()
    (directory / "config.json").write_text(json.dumps({"id2label": SCRAMBLED_ID2LABEL}), encoding="utf-8")
    (directory / "tokenizer.json").write_text("{}", encoding="utf-8")

    session = FakeSession()
    tokenizer = FakeTokenizer()
    seen: dict = {}

    def fake_session(path, providers=None):
        seen["path"] = path
        seen["providers"] = providers
        return session

    monkeypatch.setattr("onnxruntime.InferenceSession", fake_session)
    monkeypatch.setattr(tokenizers.Tokenizer, "from_file", staticmethod(lambda path: tokenizer))

    encoder = Encoder.from_model_dir(directory)

    assert encoder.labels == EXPECTED_LABELS
    # CPU only: the service ships without CUDA and must not probe a GPU.
    assert seen["providers"] == ["CPUExecutionProvider"]
    assert seen["path"].endswith("model_quantized.onnx")
    assert tokenizer.truncation == 128


def test_importing_the_module_pulls_in_no_heavy_runtime():
    """Import must stay cheap: no session, no model, no torch at module level."""
    code = (
        "import sys, app.onnx_encoder; "
        "print(sorted(m for m in ('onnxruntime', 'tokenizers', 'torch', 'transformers') if m in sys.modules))"
    )
    completed = subprocess.run(
        [sys.executable, "-c", code], cwd=SERVICE_DIR, capture_output=True, text=True, encoding="utf-8"
    )
    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.strip() == "[]"


def test_apply_neutral_band_inside_the_band_is_neutral():
    # |0.7499 - 0.5| = 0.2499 < 0.25 -> no opinion
    assert apply_neutral_band([[0.2501, 0.7499]], tau=0.25, positive_index=1) == ["neutral"]


def test_apply_neutral_band_outside_the_band_states_an_opinion():
    assert apply_neutral_band([[0.25, 0.75]], tau=0.25, positive_index=1) == ["positive"]
    assert apply_neutral_band([[0.9, 0.1]], tau=0.25, positive_index=1) == ["negative"]


def test_apply_neutral_band_boundary_equal_to_tau_states_an_opinion():
    """|p - 0.5| == tau exactly: the band is strict, so this still has a sign.

    With `<=` these two would silently become neutral, which is the mistake the
    boundary test exists to catch.
    """
    assert apply_neutral_band([[0.75, 0.25]], tau=0.25, positive_index=0) == ["positive"]
    assert apply_neutral_band([[0.25, 0.75]], tau=0.25, positive_index=0) == ["negative"]


def test_apply_neutral_band_uses_the_positive_column_index():
    """Same row, two configurations: the positive column decides the sign."""
    row = [[0.8, 0.2]]
    assert apply_neutral_band(row, tau=0.1, positive_index=0) == ["positive"]
    assert apply_neutral_band(row, tau=0.1, positive_index=1) == ["negative"]
