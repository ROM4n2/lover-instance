"""Health probe for the nlp-service.

`status` tracks whether the dictionary lexicon finished loading ("loading" ->
"ready"), which is the only readiness signal this service has at start-up: model
weights are not loaded until a batch actually asks for one.

`model_version` is the **default** version — what `/score` uses when the caller
sends `model_version: ""`. It is reported here rather than only echoed back from
/score for the caller's convenience: every score is stored and later printed in
the report under a model version, so the caller has to learn the version from the
service that actually holds the lexicon; a constant repeated on the caller's side
would be a second source of truth that drifts the first time the lexicon changes.

`available_versions` is the menu the caller may ask for: the dictionary version
plus every version in `model_registry`. It is a **configuration** list, not a
readiness report — it is non-empty while `status` is still "loading", because it
answers "what can this build score with", not "is it ready yet". `model_version`
stays `None` until the lexicon is loaded, and that remains the readiness signal.

`model_loaded` stays False: nothing is preloaded by design. A health probe is
polled constantly, so it must not open a 100 MB session or start a download —
this module reads the registry's static table and nothing else.
"""

from __future__ import annotations

from fastapi import APIRouter
from pydantic import BaseModel, Field

from app.model_registry import versions as model_versions
from app.sentiment_lexicon import MODEL_VERSION

# M1-dictionary readiness only. This dict tracks the lexicon, not model weights.
_LEXICON_STATE: dict = {"loaded": False, "version": None}


def mark_lexicon_loaded(version: str) -> None:
    """Called by the lexicon module once its word lists are in memory."""
    _LEXICON_STATE["loaded"] = True
    _LEXICON_STATE["version"] = version


class Health(BaseModel):
    status: str
    model_loaded: bool
    model_version: str | None = None
    available_versions: list[str] = Field(default_factory=list)


def available_versions() -> list[str]:
    """Dictionary first, then the configured models, in configuration order.

    Built from the constant rather than from `_LEXICON_STATE` so the menu does not
    change shape with readiness; a caller choosing a version needs the same list
    before and after the lexicon finishes loading.
    """
    return [MODEL_VERSION, *model_versions()]


router = APIRouter()


@router.get("/health", response_model=Health)
def health() -> Health:
    status = "ready" if _LEXICON_STATE["loaded"] else "loading"
    return Health(
        status=status,
        model_loaded=False,
        model_version=_LEXICON_STATE["version"],
        available_versions=available_versions(),
    )
