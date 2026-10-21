"""M5 Task 3: the registry the `lover model` commands read, and its one download.

Pinned here, offline, on a temporary cache root:

- `registry` is the complete version table — every configured version, its pinned
  files and its revision — and it downloads exactly never. The double for
  `ensure_model` raises the moment it is called, so "no network" is asserted by
  something that cannot pass by accident.
- `ensure` fetches only what is missing, verifies every byte by sha256 and
  reports the path plus each digest. A second call over a complete cache fetches
  nothing.
- Bytes that do not match the pin raise and leave nothing behind: neither the
  final name nor the `.part` file survives, so the next start retries instead of
  running on garbage.
- An unknown version is a usage error (exit 2) that names the menu.

The registry is replaced with a one-entry table whose pins match the bytes the
fake fetch writes: the real pins are digests of real weight files, and a test
that had to satisfy them would have to download 100 MB to learn a sha256.
"""

from __future__ import annotations

import hashlib
import json

import pytest

from app import model_cli, model_registry, model_store
from app.model_registry import ModelEntry
from app.model_store import ModelIntegrityError, ModelSpec

VERSION = "test-model-v1"

CONFIG_BYTES = b'{"id2label": {"0": "Negative", "1": "Positive"}}'
MODEL_BYTES = b"onnx-weights-bytes"
TOKENIZER_BYTES = b'{"vocab": {}}'

CONTENTS = {
    "config.json": CONFIG_BYTES,
    "model_quantized.onnx": MODEL_BYTES,
    "tokenizer.json": TOKENIZER_BYTES,
}


def digest(content: bytes) -> str:
    return hashlib.sha256(content).hexdigest()


TEST_SPEC = ModelSpec(
    name="test-model",
    revision="rev-1",
    files={name: digest(content) for name, content in CONTENTS.items()},
)

TEST_ENTRY = ModelEntry(version=VERSION, spec=TEST_SPEC, positive_index=1, tau=0.45)


@pytest.fixture
def cache(tmp_path, monkeypatch):
    """A cache root that is never the real `%LOCALAPPDATA%`."""
    root = tmp_path / "cache"
    monkeypatch.setattr(model_store, "cache_root", lambda: root)
    return root


@pytest.fixture
def registry(monkeypatch):
    """A one-entry registry whose pins match the bytes the fake fetch writes."""
    monkeypatch.setattr(model_registry, "MODELS", {VERSION: TEST_ENTRY})
    return TEST_ENTRY


def fake_fetch(contents: dict[str, bytes], recorder: list[str] | None = None):
    """A `fetch` that writes `contents` — the seam `ensure_model` documents."""

    def fetch(url: str, destination, timeout: float) -> None:
        name = destination.name.removesuffix(".part")
        if recorder is not None:
            recorder.append(name)
        destination.write_bytes(contents[name])

    return fetch


def ensure_with_fetch(monkeypatch, contents: dict[str, bytes]) -> list[str]:
    """Run the real `ensure_model` with a fake `fetch`; return what it fetched."""
    fetched: list[str] = []
    real = model_store.ensure_model

    def ensure(spec, **kwargs):
        return real(spec, fetch=fake_fetch(contents, fetched), **kwargs)

    monkeypatch.setattr(model_store, "ensure_model", ensure)
    return fetched


def seed(cache, names=CONTENTS.keys()) -> None:
    """Put already-verified files in the cache, as a previous run would."""
    directory = cache / TEST_SPEC.name
    directory.mkdir(parents=True, exist_ok=True)
    for name in names:
        (directory / name).write_bytes(CONTENTS[name])


def test_registry_lists_the_version_with_its_pinned_files(cache, registry):
    payload = model_cli.registry_payload()

    assert payload["cache_root"] == str(cache)
    assert [model["version"] for model in payload["models"]] == [VERSION]
    (model,) = payload["models"]
    assert model["name"] == TEST_SPEC.name
    assert model["revision"] == TEST_SPEC.revision
    assert model["files"] == TEST_SPEC.files


def test_registry_downloads_nothing(cache, registry, monkeypatch):
    """The menu is not the installation.

    `ensure_model` is replaced with something that raises: a listing that quietly
    downloaded 100 MB would pass every other assertion in this file.
    """

    def explode(*args, **kwargs):
        raise AssertionError("registry must not download model weights")

    monkeypatch.setattr(model_store, "ensure_model", explode)

    payload = model_cli.registry_payload()

    assert payload["models"], "the registry table is empty"


def test_ensure_fetches_only_what_is_missing_and_verifies_it(cache, registry, monkeypatch):
    seed(cache, ["config.json"])  # already correct: not fetched again
    fetched = ensure_with_fetch(monkeypatch, CONTENTS)

    payload = model_cli.ensure_payload(VERSION)

    assert fetched == ["model_quantized.onnx", "tokenizer.json"]
    assert payload["version"] == VERSION
    assert payload["path"] == str(cache / TEST_SPEC.name)
    assert payload["fetched"] == ["model_quantized.onnx", "tokenizer.json"]
    assert {file["name"]: file["sha256"] for file in payload["files"]} == TEST_SPEC.files


def test_ensure_over_a_complete_cache_makes_no_request(cache, registry, monkeypatch):
    seed(cache)
    fetched = ensure_with_fetch(monkeypatch, CONTENTS)

    payload = model_cli.ensure_payload(VERSION)

    assert fetched == [], "a complete cache must issue no request at all"
    assert payload["fetched"] == []
    assert payload["path"] == str(cache / TEST_SPEC.name)


def test_ensure_refuses_bytes_that_do_not_match_the_pin_and_leaves_no_file(
    cache, registry, monkeypatch
):
    wrong = dict(CONTENTS)
    wrong["model_quantized.onnx"] = b"truncated-or-corrupt-bytes"
    ensure_with_fetch(monkeypatch, wrong)

    with pytest.raises(ModelIntegrityError) as excinfo:
        model_cli.ensure_payload(VERSION)

    assert excinfo.value.expected == TEST_SPEC.files["model_quantized.onnx"]
    directory = cache / TEST_SPEC.name
    assert not (directory / "model_quantized.onnx").exists(), (
        "a file that failed its digest must not wear the final name"
    )
    assert not (directory / "model_quantized.onnx.part").exists(), (
        "the partial download must be removed, or the next run hashes it again"
    )
    # The files that were already correct survive: a failed repair of one file
    # must not cost the user the ones that were fine.
    assert (directory / "config.json").exists()


def test_ensure_unknown_version_is_a_usage_error(cache, registry, capsys):
    code = model_cli.main(["ensure", "not-a-model"])

    assert code == 2
    err = capsys.readouterr().err
    assert "not-a-model" in err
    assert VERSION in err, "the message must name the versions that do exist"


def test_registry_subcommand_prints_json(cache, registry, capsys):
    assert model_cli.main(["registry"]) == 0
    payload = json.loads(capsys.readouterr().out)
    assert [model["version"] for model in payload["models"]] == [VERSION]


def test_ensure_subcommand_prints_json(cache, registry, monkeypatch, capsys):
    ensure_with_fetch(monkeypatch, CONTENTS)

    assert model_cli.main(["ensure", VERSION]) == 0
    payload = json.loads(capsys.readouterr().out)

    assert payload["version"] == VERSION
    assert {file["name"]: file["sha256"] for file in payload["files"]} == TEST_SPEC.files


def test_lexicon_probe_reports_success_with_the_dictionary_version(capsys):
    """The zero-model promise, checked on a machine that never had a weight.

    The polarity is asserted positive rather than merely present: a probe that
    silently no-opped on empty input would also print `ok: true`, and only a sign
    on a clearly happy sentence tells "scored" from "did nothing".
    """
    from app import sentiment_lexicon

    code = model_cli.main(["lexicon-probe"])
    payload = json.loads(capsys.readouterr().out)

    assert code == 0
    assert payload["ok"] is True
    assert payload["needs_weights"] is False
    assert payload["version"] == sentiment_lexicon.MODEL_VERSION
    assert payload["polarity"] > 0, "今天特别开心 must score positive, not neutral"


def test_lexicon_probe_never_touches_the_weight_download(capsys, monkeypatch):
    """`ensure_model` replaced with a landmine: the probe must never step on it.

    This is the whole read-only guarantee. If the probe ever routed through the
    model branch it would call `ensure_model`, which downloads — and it would do
    so on the path a person runs precisely because their cache is empty.
    """

    def explode(*args, **kwargs):
        raise AssertionError("lexicon-probe must not depend on model weights")

    monkeypatch.setattr(model_store, "ensure_model", explode)

    code = model_cli.main(["lexicon-probe"])
    payload = json.loads(capsys.readouterr().out)

    assert code == 0
    assert payload["ok"] is True


def test_lexicon_probe_reports_failure_when_the_dictionary_raises(capsys, monkeypatch):
    """A broken dictionary is `ok: false` and a non-zero exit, not a traceback.

    The synthetic input is our own literal, so even in the failure path no chat
    content can appear in the message — asserted, not merely intended.
    """
    import app.score as score_module

    def explode(text):
        raise RuntimeError("dictionary data missing")

    monkeypatch.setattr(score_module, "score", explode)

    code = model_cli.main(["lexicon-probe"])
    payload = json.loads(capsys.readouterr().out)

    assert code != 0
    assert payload["ok"] is False
    assert "dictionary data missing" in payload["error"]
    assert model_cli.PROBE_TEXT not in payload["error"]
