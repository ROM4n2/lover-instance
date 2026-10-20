"""Batched scoring contract between Go and the nlp-service.

The request carries text and nothing else: no `is_self`, no sender, no talker.
Only Go knows who sent a message, so splitting words and expressions between
self and partner is Go's aggregation job, not this service's. Symmetrically the
response carries no `top_words` — per-item `tokens` are returned and Go counts
them.

`model_version` selects the scorer, and it is a closed set:

- `""` means "whatever this service scores with by default", i.e. the dictionary
  baseline. It exists so a caller can ask for "the default" without hardcoding a
  version string that would drift from the service's own.
- the dictionary version — the baseline path, unchanged since M1.
- a version in `model_registry` — an ONNX model, scored from probabilities.
- anything else — **400**. Never the dictionary. Every score is stored and later
  printed under `model_version`, so silently answering an unknown version with
  dictionary guesses would put the baseline's output in a model's column, which
  is precisely the kind of unlabelled substitution ADR-0004 forbids.

A model whose weights cannot be fetched or built *yet* is **503**, not 400 and
not 500: the request was well formed and the version exists, the service is just
not able to serve it right now. Deliberately a short list — a dropped connection,
a full disk, bytes that did not match the pin on this attempt — and it is logged
before it leaves, because a 503 that Go retries three times and then gives up on
is otherwise invisible: the caller reads only `code`, and the service had no
trace of it at all.

Everything else that can go wrong while loading is **permanent**: an empty pinned
spec, a `positive_index` outside the model's columns, a `config.json` that is not
JSON. Those are configuration bugs, and they leave as 500 with uvicorn's
traceback rather than as a 503 the caller would dutifully retry.

Nothing here writes to a file or a database. `score_batch` is a pure function in
the ordinary sense — same input, same output — with one caveat the model path
makes explicit: the first call for a version builds and caches an encoder
(`model_registry.load`), and later calls reuse it. Output does not depend on
whether the encoder was already warm.
"""

from __future__ import annotations

import logging
import urllib.error

from fastapi import APIRouter, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel, ConfigDict

from app import model_registry
from app.health import mark_lexicon_loaded
from app.model_store import ModelIntegrityError
from app.onnx_encoder import NEUTRAL_LABEL, POSITIVE_LABEL, apply_neutral_band
from app.sentiment_lexicon import MODEL_VERSION, score
from app.tokenize import countable_tokens

# Importing app.sentiment_lexicon above loaded and validated every word list;
# a missing or malformed list raises there, loudly. So by this point the
# dictionary baseline is genuinely ready and /health may report "ready".
mark_lexicon_loaded(MODEL_VERSION)

MAX_ITEMS = 256
MAX_BODY_BYTES = 5 * 1024 * 1024

# Probability mass on the positive column below which a binary model's opinion is
# recorded as negative, and above which as positive. The band in between is
# `tau` wide and belongs to the entry, not to this module: it is measured per
# model (see `model_registry`).
NEUTRAL_POLARITY = 0.0

# Failures that mean "not servable *yet*", and the only ones allowed to become a
# retryable 503:
#
# - `OSError` — a full disk, a refused socket, a permission problem, a half-written
#   file. `urllib.error.URLError` is a subclass of it and is named too, because
#   that is the failure a reader of this list is actually looking for.
# - `ModelIntegrityError` — this attempt downloaded bytes that did not match the
#   pin. Not permanent by construction: the next attempt starts over.
#
# Deliberately excluded: `onnxruntime`'s graph/session errors. Importing the module
# here would undo this package's "import is cheap, onnxruntime is lazy" property,
# and after `model_store` has verified every file by sha256 a graph that will not
# build points at a wrong pin or a wrong export — a permanent configuration bug
# that deserves the 500 (and the traceback) instead of a retried download. Same
# reasoning for `ValueError` from a `config.json`/`tokenizer.json` that does not
# parse, for the empty-spec check, and for `KeyError` on a version that is not
# configured.
RETRYABLE_LOAD_ERRORS = (OSError, urllib.error.URLError, ModelIntegrityError)

logger = logging.getLogger(__name__)

router = APIRouter()


class Item(BaseModel):
    """One message to score. Extra keys are rejected on purpose.

    `extra="forbid"` is the enforcement arm of "no identity in the request":
    a caller that tries to smuggle `is_self` or `sender` in gets a 422 instead
    of having it silently ignored.
    """

    model_config = ConfigDict(extra="forbid")

    msg_id: int
    content: str


class ScoreRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    model_version: str
    items: list[Item]


class ResultItem(BaseModel):
    msg_id: int
    polarity: float
    emotion: str
    intensity: float
    tokens: list[str]


class ScoreResponse(BaseModel):
    model_version: str
    results: list[ResultItem]


class UnknownModelVersion(ValueError):
    """A version that is neither the dictionary nor a configured model.

    Carries only the version name and the menu: the message ends up in an HTTP
    body, and no error may ever quote chat content.
    """

    def __init__(self, version: str, available: list[str]):
        super().__init__(f"unknown model_version {version!r}; available: {', '.join(available)}")
        self.version = version
        self.available = available


class ModelUnavailable(RuntimeError):
    """The version exists but its weights could not be obtained or loaded *yet*.

    Distinct from `UnknownModelVersion` on purpose: this one is retryable, and
    the caller maps it to 503 rather than asking the user to fix a version name.
    Only `_load_encoder` raises it, and only for `RETRYABLE_LOAD_ERRORS`; every
    other load failure, and every violated invariant downstream, is a plain
    exception so that it surfaces as a 500.
    """


def resolve_version(model_version: str) -> str:
    """The version this request will actually be scored under, or raise.

    `""` resolves to the dictionary version, which is the only silent
    substitution allowed: the caller asked for "the default" by name.
    """
    version = model_version or MODEL_VERSION
    if version == MODEL_VERSION or model_registry.get(version) is not None:
        return version
    raise UnknownModelVersion(version, [MODEL_VERSION, *model_registry.versions()])


def _lexicon_result(item: dict) -> dict:
    content = item["content"] or ""
    result = score(content)
    return {
        "msg_id": int(item["msg_id"]),
        "polarity": result.polarity,
        "emotion": result.emotion,
        "intensity": result.intensity,
        "tokens": countable_tokens(content),
    }


def _negative_index(labels: list[str], positive_index: int) -> int | None:
    """The column the model calls negative, read from its own label order.

    Matched on the name rather than assumed to be "the other one", because a
    three-class export puts a neutral column in the middle: `1 - positive_index`
    would read Neutral as Negative for distilbert and report `-0.8` for a shrug.
    """
    for index, label in enumerate(labels):
        if index != positive_index and label.lower().startswith("neg"):
            return index
    return None


def _polarity(row: list[float], labels: list[str], entry: model_registry.ModelEntry) -> float:
    """Signed polarity in [-1, 1] for one row of class probabilities.

    Binary model: the band decision is `apply_neutral_band`'s, not a second copy
    of it here. That function has always been strict (`|p - 0.5| < tau`, boundary
    still speaks) and Task 1's mutation tests pin it that way; deriving
    `p > 0.5 + tau` / `p < 0.5 - tau` locally read the boundary the other way
    round, so the same model scored `0.0` here and `positive` there. Outside the
    band the magnitude is the *winning side's* probability (`p` or `1 - p`):
    using `p` for a positive and `-(1 - p)` for a negative keeps `|polarity|`
    monotone in confidence and never lets the losing class add magnitude — a
    0.51/0.49 split should look weak, not strong.

    Multi-class model: argmax, plus for positive, minus for negative, exactly 0.0
    for its own neutral column. No band is applied; `tau` is 0 for these entries.
    """
    width = len(row)
    if width < 2:
        # Not "cannot be served right now" but "the export is broken": a 500, so
        # the bug is visible instead of being retried as an outage.
        raise RuntimeError(f"{entry.version}: model returned {width} class column(s)")

    if width == 2:
        label = apply_neutral_band([row], entry.tau, entry.positive_index)[0]
        if label == NEUTRAL_LABEL:
            return NEUTRAL_POLARITY
        if label == POSITIVE_LABEL:
            return row[entry.positive_index]
        return -(1.0 - row[entry.positive_index])

    best = max(range(width), key=lambda index: row[index])
    if best == entry.positive_index:
        return row[best]
    negative = _negative_index(labels, entry.positive_index)
    if negative is None:
        # Without a `neg*` column the winning class cannot be signed, and returning
        # 0.0 would file a confident negative prediction as "no opinion".
        raise RuntimeError(
            f"{entry.version}: no negative column among {labels!r}; "
            f"argmax column {best} cannot be signed"
        )
    if best == negative:
        return -row[best]
    return NEUTRAL_POLARITY


def _load_encoder(entry: model_registry.ModelEntry):
    """`model_registry.load`, turning only *retryable* failures into a 503.

    `RETRYABLE_LOAD_ERRORS` is the whole list, and everything else propagates: a
    permanent misconfiguration (empty spec, `positive_index` out of range, a
    version missing from `MODELS`, a config that will not parse) becomes a 500
    with uvicorn's traceback. It used to be a blanket `except Exception`, which
    made Go retry a broken pin three times and left no record of why.

    The log line carries the version, the exception type and (via `exc_info`) the
    traceback. Never content: no chat text is in scope this early, and the rule
    holds anyway.
    """
    try:
        return model_registry.load(entry.version)
    except RETRYABLE_LOAD_ERRORS as exc:
        logger.warning(
            "model unavailable: version=%s error=%s",
            entry.version,
            type(exc).__name__,
            exc_info=True,
        )
        raise ModelUnavailable(f"{entry.spec.name}: {type(exc).__name__}: {exc}") from exc


def _model_results(items: list[dict], entry: model_registry.ModelEntry) -> list[dict]:
    """Score with a model. `emotion` stays empty: it has no DUTIR categories.

    A 2- or 3-class polarity says nothing about which of `好/乐/哀/怒/惧/恶/惊` was
    felt, and filling one in would be a fabrication with a plausible shape. Go
    groups the model version by polarity sign; the radar chart stays
    dictionary-only (Task 7).
    """
    encoder = _load_encoder(entry)

    contents = [item["content"] or "" for item in items]
    # Blank text is "no opinion", not a prediction. The encoder would answer
    # anyway, and a real model asked about "" produces confident noise; keeping
    # blanks out of the batch also matches the dictionary path's output.
    scored_at = [index for index, text in enumerate(contents) if text.strip()]

    polarities: dict[int, float] = {}
    if scored_at:
        rows = encoder.probabilities([contents[index] for index in scored_at])
        if len(rows) != len(scored_at):
            # Misaligned rows would mislabel every later message in the block.
            # A retry cannot fix that, so it is a 500, not a `ModelUnavailable`.
            raise RuntimeError(
                f"{entry.version}: {len(rows)} probability rows for {len(scored_at)} texts"
            )
        for index, row in zip(scored_at, rows):
            polarities[index] = _polarity(list(row), encoder.labels, entry)

    results: list[dict] = []
    for index, item in enumerate(items):
        polarity = polarities.get(index, NEUTRAL_POLARITY)
        results.append(
            {
                "msg_id": int(item["msg_id"]),
                "polarity": polarity,
                "emotion": "",
                # Same meaning as the dictionary path's intensity in one respect
                # only: 0 means "no opinion". Here it is |polarity| — confidence,
                # not a degree-adverb factor.
                "intensity": abs(polarity),
                "tokens": countable_tokens(contents[index]),
            }
        )
    return results


def score_batch(items: list[dict], model_version: str = "") -> list[dict]:
    """Score a batch of `{"msg_id": int, "content": str}` dicts.

    Pure: no file writes, no database, no mutable global state beyond the
    encoder cache (see the module docstring). Raises `UnknownModelVersion` rather
    than falling back to the dictionary — the failure mode this signature exists
    to prevent — and `ModelUnavailable` when the weights cannot be used.
    """
    version = resolve_version(model_version)
    entry = model_registry.get(version)
    if entry is None:
        return [_lexicon_result(item) for item in items]
    return _model_results(items, entry)


def _too_large(code: str, detail: str) -> JSONResponse:
    # Never echo content: only counts and limit names leak into errors.
    return JSONResponse(status_code=413, content={"code": code, "detail": detail})


@router.post("/score", response_model=ScoreResponse)
async def score_endpoint(request: Request, payload: ScoreRequest) -> ScoreResponse | JSONResponse:
    body_length = len(await request.body())
    if body_length > MAX_BODY_BYTES:
        return _too_large("payload_too_large", f"body exceeds {MAX_BODY_BYTES} bytes")

    if len(payload.items) > MAX_ITEMS:
        return _too_large("too_many_items", f"items exceeds {MAX_ITEMS}")

    # Resolved once, here, so that the version reported in the response is the
    # version that actually scored. An empty string means "the default".
    version = payload.model_version or MODEL_VERSION
    try:
        results = score_batch([item.model_dump() for item in payload.items], version)
    except UnknownModelVersion as exc:
        # 400, not 200-with-dictionary: see the module docstring.
        return JSONResponse(
            status_code=400, content={"code": "unknown_model_version", "detail": str(exc)}
        )
    except ModelUnavailable as exc:
        return JSONResponse(status_code=503, content={"code": "model_unavailable", "detail": str(exc)})

    return ScoreResponse(
        model_version=version,
        results=[ResultItem(**result) for result in results],
    )
