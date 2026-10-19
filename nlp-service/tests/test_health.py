"""Task 4 RED: FastAPI scaffold + GET /health.

Contract pinned here:
- GET /health -> 200 with exactly the keys {status, model_loaded}
- module-level `app` exists so `uvicorn app.main:app` works
- the service never imports sqlite3 (hard spec constraint: Python never opens the db)
"""

import ast
import pathlib

from fastapi.testclient import TestClient

from app.main import app, create_app

APP_DIR = pathlib.Path(__file__).resolve().parents[1] / "app"


def test_create_app_returns_fastapi_instance():
    instance = create_app()
    assert instance is not None
    assert instance is not app


def test_module_level_app_exists_for_uvicorn():
    # `uvicorn app.main:app` resolves this exact attribute.
    assert app is not None


def test_health_returns_status_and_model_loaded():
    client = TestClient(create_app())
    resp = client.get("/health")
    assert resp.status_code == 200
    body = resp.json()
    assert set(body.keys()) == {"status", "model_loaded", "model_version"}
    assert body["status"] in {"loading", "ready"}
    assert isinstance(body["model_loaded"], bool)


def test_health_reports_the_lexicon_version_it_will_score_with():
    """The caller records this string as the provenance of every score, so it
    has to come from the process that holds the lexicon rather than from a
    constant repeated on the caller's side."""
    from app import health as health_module
    from app.sentiment_lexicon import MODEL_VERSION

    client = TestClient(create_app())
    # The lexicon state is process-global and shared across tests, so the test
    # sets it directly instead of depending on which module another test
    # happened to import first.
    health_module._LEXICON_STATE["version"] = None
    health_module._LEXICON_STATE["loaded"] = False
    try:
        body = client.get("/health").json()
        assert body["status"] == "loading"
        assert body["model_version"] is None, "a loading service must not name a version"

        health_module.mark_lexicon_loaded(MODEL_VERSION)
        body = client.get("/health").json()
        assert body["status"] == "ready"
        assert body["model_version"] == MODEL_VERSION
    finally:
        health_module._LEXICON_STATE["version"] = MODEL_VERSION
        health_module._LEXICON_STATE["loaded"] = True


def test_health_never_touches_sqlite():
    """AST-scan app/ and assert no `sqlite3` import appears anywhere."""
    offenders = []
    files = sorted(APP_DIR.rglob("*.py"))
    assert files, "app/ package must contain python modules"
    for path in files:
        tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                for alias in node.names:
                    if alias.name.split(".")[0] == "sqlite3":
                        offenders.append(f"{path.name}: import {alias.name}")
            elif isinstance(node, ast.ImportFrom):
                module = node.module or ""
                if module.split(".")[0] == "sqlite3":
                    offenders.append(f"{path.name}: from {module} import ...")
    assert offenders == [], f"sqlite3 import found in app/: {offenders}"