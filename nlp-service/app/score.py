"""Batched scoring contract between Go and the nlp-service.

The request carries text and nothing else: no `is_self`, no sender, no talker.
Only Go knows who sent a message, so splitting words and expressions between
self and partner is Go's aggregation job, not this service's. Symmetrically the
response carries no `top_words` — per-item `tokens` are returned and Go counts
them.

Nothing here writes to a file or a database. `score_batch` is a pure function:
same input, byte-identical output.
"""

from __future__ import annotations

from fastapi import APIRouter, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel, ConfigDict

from app.health import mark_lexicon_loaded
from app.sentiment_lexicon import MODEL_VERSION, score
from app.tokenize import tokenize

# Importing app.sentiment_lexicon above loaded and validated every word list;
# a missing or malformed list raises there, loudly. So by this point the
# dictionary baseline is genuinely ready and /health may report "ready".
mark_lexicon_loaded(MODEL_VERSION)

MAX_ITEMS = 256
MAX_BODY_BYTES = 5 * 1024 * 1024

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


def score_batch(items: list[dict]) -> list[dict]:
    """Score a batch of `{"msg_id": int, "content": str}` dicts.

    Pure: no file writes, no database, no mutable global state.
    """
    results: list[dict] = []
    for item in items:
        content = item["content"] or ""
        result = score(content)
        results.append(
            {
                "msg_id": int(item["msg_id"]),
                "polarity": result.polarity,
                "emotion": result.emotion,
                "intensity": result.intensity,
                "tokens": tokenize(content),
            }
        )
    return results


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

    results = score_batch([item.model_dump() for item in payload.items])
    return ScoreResponse(
        model_version=payload.model_version or MODEL_VERSION,
        results=[ResultItem(**result) for result in results],
    )