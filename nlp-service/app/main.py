"""FastAPI entrypoint for the nlp-service.

Stateless text scoring only: no database file is ever opened here.
"""

from __future__ import annotations

import sys

# MUST stay at module top level. `uvicorn app.main:app` imports this module
# but never executes an `if __name__ == "__main__":` block, so a guard-scoped
# reconfigure would never run and a Windows GBK console could crash on output.
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")

from fastapi import FastAPI  # noqa: E402

from app.health import router as health_router  # noqa: E402


def create_app() -> FastAPI:
    """Factory so tests can build an isolated instance per test."""
    application = FastAPI(title="lover-instance nlp-service", version="m1")
    application.include_router(health_router)
    return application


# Module-level instance: `uvicorn app.main:app` resolves this exact attribute.
app = create_app()