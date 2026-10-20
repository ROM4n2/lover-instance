"""The whitelist of model versions `/score` will accept, and their lazy loader.

WHY A WHITELIST, NOT A LOOKUP BY NAME
-------------------------------------
`model_version` is the identity every stored score is filed under. Accepting an
unknown name would force a choice between two wrong answers: refuse the batch, or
score it with whatever happens to be loaded. Only the first is honest, so the set
of versions is a closed, reviewed list — a port, not a probe.

WHAT AN ENTRY SAYS ABOUT A MODEL
--------------------------------
- `spec` — the pinned files (`model_store.ModelSpec`), so "which weights" is a
  digest, not a repository name that moves under you.
- `positive_index` — which output column is P(positive). Read from the export's
  `id2label`; it is 1 for Erlangshen and 0 for the multilingual distilbert, and
  getting it backwards flips the sign of every score in the report.
- `tau` — half-width of the "no opinion" band for a binary model; `0.0` marks a
  model that has its own neutral column and therefore needs no band.
- `max_length` — the truncation cap. Both candidates are trained at 512, but a
  128-token cap bounds the cost of one absurd row without touching ordinary chat.

TAU IS MEASURED, NOT CHOSEN
---------------------------
`0.45` for `erlangshen-110m-v1` was selected on the 174-sentence set in
`workmem/eval/BAKEOFF.md`, and those labels are **agent-draft**: the trade it
encodes (τ=0.45 nearly eliminates reversed calls, at the cost of swallowing much
of `affection` into the neutral band) is a trade against a model of the user's
judgement, not against the user's own labels. **Re-select τ once the user has
relabelled the set**, and treat the current value as provisional — it is
recorded here so that it is at least visible at the point of use.

CACHING IS PROCESS STATE, DELIBERATELY
--------------------------------------
Importing this module loads nothing: no weights, no session, no network. The
first `/score` for a version pays for the download and the session, and every
later batch reuses them. That keeps `/health` cheap (it is polled constantly) and
keeps a service that never sees a model request from downloading 240 MB.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass

from app.model_store import ModelSpec, ensure_model
from app.onnx_encoder import Encoder


@dataclass(frozen=True)
class ModelEntry:
    """One reviewed model, as much as the scoring path needs to know about it."""

    version: str
    spec: ModelSpec
    positive_index: int
    tau: float
    max_length: int = 128


# Erlangshen-Roberta-110M-Sentiment, int8 ONNX. 2 classes, Negative at 0.
_ERLANGSHEN = ModelEntry(
    version="erlangshen-110m-v1",
    spec=ModelSpec(
        name="erlangshen-110m-sentiment",
        revision="main",
        files={
            "config.json": "555b1009d9828e3fbb284bfd39f488ecc0aedadfb377381347e3c532b7803436",
            "model_quantized.onnx": "5e0e07161a093d7134f0a3293ae6c3c6e9bae2d73c6bfbba65ae26e3a512f466",
            "tokenizer.json": "7dfbf1966ebf99d471c3796e9b457329d2b2182b817e144f1e904b957745c839",
        },
    ),
    positive_index=1,
    # Selected on workmem/eval/BAKEOFF.md's 174 agent-draft labels: it cuts
    # reversed calls to 1/174 while dropping `affection` recall. Provisional
    # until the user relabels the evaluation set — see the module docstring.
    tau=0.45,
)

# distilbert-base-multilingual-cased-sentiments-student, int8 ONNX. 3 classes,
# positive at 0, with its own neutral column (so no band).
_DISTILBERT = ModelEntry(
    version="distilbert-multilingual-v1",
    spec=ModelSpec(
        name="distilbert-multilingual-sentiments",
        revision="main",
        files={
            "config.json": "89c0158e3725d30d852258638c64057228ff93d676f2c3e22ac20025a89653c7",
            "model_quantized.onnx": "9570fdd726d952dbaf559ba0b9f38a523554f08a8038d62d9263a90dcbb3cd3e",
            "tokenizer.json": "a4a4fa4b10a2a40361529954532f10257fda53cb22177554a862bcc661b7e4d8",
        },
    ),
    positive_index=0,
    tau=0.0,
)

# Only real candidates live here: an entry is a promise that the weights exist,
# are pinned, and have been run against the evaluation set at least once.
MODELS: dict[str, ModelEntry] = {
    _ERLANGSHEN.version: _ERLANGSHEN,
    _DISTILBERT.version: _DISTILBERT,
}

# Built encoders, keyed by version. Guarded by `_LOCK` for the whole build:
# `ensure_model` writes `<file>.part` and promotes it with `os.replace`, which
# two threads doing at once would turn into a corrupted download.
_CACHE: dict[str, Encoder] = {}
_LOCK = threading.Lock()


def versions() -> list[str]:
    """The model versions this build can score with, in configuration order.

    Read-only and cheap: `available_versions` on `/health` comes from here, so a
    health probe must never force a load.
    """
    return list(MODELS)


def get(version: str) -> ModelEntry | None:
    """The entry for `version`, or None if it is not a configured model."""
    return MODELS.get(version)


def load(version: str) -> Encoder:
    """The encoder for `version`, downloading and building it at most once.

    Raises `KeyError` for a version that is not configured (`score.py` checks
    `get` first, so this only fires for a direct, unvalidated call) and lets
    `model_store` / `onnxruntime` failures through unchanged: turning them into
    something friendlier here would hide which layer failed.

    An entry that pins no files is a `ValueError`, not a load: `ensure_model`
    would satisfy an empty spec by returning an empty directory (fail-open), and
    the real fault would then surface far away as an onnxruntime complaint about
    a missing `config.json`. Task 2 recorded this and left the check to the
    wiring; `score.py` turns a `ValueError` into a 500, so the misconfiguration
    is visible instead of retried.
    """
    entry = MODELS[version]

    if not entry.spec.files:
        raise ValueError(
            f"{version}: spec pins no files, so there is nothing to verify or load"
        )

    cached = _CACHE.get(version)
    if cached is not None:
        return cached

    with _LOCK:
        # Re-check under the lock: a second thread may have built it while this
        # one waited, and building it again would open a second 100 MB session.
        cached = _CACHE.get(version)
        if cached is not None:
            return cached

        encoder = Encoder.from_model_dir(ensure_model(entry.spec))
        # Task 1's recorded hazard: a negative `positive_index` does not raise,
        # it silently reads the other column and reverses every sign. Fail here,
        # at the one place that knows both the entry and the model's real width.
        if not 0 <= entry.positive_index < len(encoder.labels):
            raise ValueError(
                f"{version}: positive_index {entry.positive_index} is outside the "
                f"{len(encoder.labels)} output columns"
            )
        if entry.max_length != encoder.max_length:
            encoder.tokenizer.enable_truncation(max_length=entry.max_length)
            encoder.max_length = entry.max_length

        _CACHE[version] = encoder
        return encoder
