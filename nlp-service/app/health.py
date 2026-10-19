"""Health probe for the nlp-service.

M1 ships no ML model at all: scoring is a self-built dictionary baseline, so
`model_loaded` is always False. `status` instead tracks whether the dictionary
lexicon finished loading ("loading" -> "ready"), which is the only real
readiness signal this service has.

`model_version` is reported here rather than only echoed back from /score for
the caller's convenience. Every score is stored and later printed in the report
under a model version, so the caller has to learn the version from the service
that actually holds the lexicon; a constant repeated on the caller's side would
be a second source of truth that drifts the first time the lexicon changes.
"""

from __future__ import annotations

from fastapi import APIRouter
from pydantic import BaseModel

# M1 loads no model weights. This dict tracks lexicon readiness only.
_LEXICON_STATE: dict = {"loaded": False, "version": None}


def mark_lexicon_loaded(version: str) -> None:
    """Called by the lexicon module once its word lists are in memory."""
    _LEXICON_STATE["loaded"] = True
    _LEXICON_STATE["version"] = version


class Health(BaseModel):
    status: str
    model_loaded: bool
    model_version: str | None = None


router = APIRouter()


@router.get("/health", response_model=Health)
def health() -> Health:
    status = "ready" if _LEXICON_STATE["loaded"] else "loading"
    return Health(status=status, model_loaded=False, model_version=_LEXICON_STATE["version"])