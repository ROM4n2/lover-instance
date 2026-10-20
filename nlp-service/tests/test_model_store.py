"""M2 Task 2 RED: fetch, verify and cache ONNX weights by a pinned sha256.

Contract pinned here; every test runs offline on a temporary cache root:

- A complete cache **issues no request at all**. The `fetch` double raises
  `AssertionError` the moment it is called, so "no network" is asserted by
  something that cannot pass by accident — not by a counter nobody reads.
- Every byte is hashed before it is allowed to take the final file name: content
  lands in `<file>.part`, and only a matching digest is promoted with
  `os.replace`. A mismatch deletes the partial file and raises, so a half-written
  weight can never be mistaken for a ready model on the next start.
- A file that exists but does not match its pinned digest is **not** a hit: it is
  downloaded again. "The name is there" is exactly the check that lets a
  truncated download survive a reboot.
- The default fetcher goes through `urllib.request.urlopen` with the caller's
  timeout, never a zero one: a zero timeout means the first start can hang
  forever, and `DEFAULT_TIMEOUT` is what ordinary callers get.
- `cache_root()` is the only code that reads the environment: `LOCALAPPDATA` on
  Windows, `XDG_CACHE_HOME` elsewhere, `~/.cache` as the last resort.
"""

from __future__ import annotations

import hashlib
import io
import pathlib
import sys
import urllib.request

import pytest

from app import model_store
from app.model_store import ModelIntegrityError, ModelSpec, ensure_model

MODEL_NAME = "pinned-model"
REVISION = "rev-1"
ENDPOINT = "https://hf-mirror.com"

MODEL_BYTES = b"onnx-weights-bytes"
TOKENIZER_BYTES = b'{"vocab": {}}'

CONTENTS = {
    "model_quantized.onnx": MODEL_BYTES,
    "tokenizer.json": TOKENIZER_BYTES,
}


@pytest.fixture
def cache(tmp_path, monkeypatch):
    """A cache root that is never the real `%LOCALAPPDATA%`."""
    root = tmp_path / "cache"
    monkeypatch.setattr(model_store, "cache_root", lambda: root)
    return root


def digest(content: bytes) -> str:
    return hashlib.sha256(content).hexdigest()


def spec_for(contents: dict[str, bytes] = CONTENTS) -> ModelSpec:
    return ModelSpec(
        name=MODEL_NAME,
        revision=REVISION,
        files={name: digest(content) for name, content in contents.items()},
    )


def seed(cache: pathlib.Path, names=CONTENTS.keys()) -> pathlib.Path:
    """Put already-verified files in the cache, as a previous run would."""
    directory = cache / MODEL_NAME
    directory.mkdir(parents=True, exist_ok=True)
    for name in names:
        (directory / name).write_bytes(CONTENTS[name])
    return directory


def expected_url(name: str) -> str:
    return f"{ENDPOINT}/{MODEL_NAME}/resolve/{REVISION}/{name}"


def exploding_fetch(url, destination, timeout):
    raise AssertionError(f"fetch must not be called on a cache hit, but got {url}")


def recording_fetch(calls: list[str], contents: dict[str, bytes] = CONTENTS):
    def fetch(url, destination, timeout):
        name = url.rsplit("/", 1)[-1]
        calls.append(url)
        destination.write_bytes(contents[name])

    return fetch


def test_complete_cache_hit_makes_no_network_request(cache):
    directory = seed(cache)

    result = ensure_model(spec_for(), fetch=exploding_fetch)

    assert result == directory
    assert sorted(path.name for path in directory.iterdir()) == sorted(CONTENTS), "no .part left behind"


def test_first_download_writes_every_file_with_the_pinned_digest(cache):
    calls: list[str] = []

    directory = ensure_model(spec_for(), fetch=recording_fetch(calls))

    assert directory == cache / MODEL_NAME
    for name, content in CONTENTS.items():
        assert (directory / name).read_bytes() == content
        assert digest((directory / name).read_bytes()) == digest(content)
    assert sorted(calls) == sorted(expected_url(name) for name in CONTENTS)
    assert not list(directory.glob("*.part"))


def test_sha256_mismatch_raises_and_leaves_no_file_under_the_final_name(cache):
    calls: list[str] = []

    def corrupted_fetch(url, destination, timeout):
        calls.append(url)
        destination.write_bytes(b"truncated-garbage")

    with pytest.raises(ModelIntegrityError) as raised:
        ensure_model(spec_for(), fetch=corrupted_fetch)

    error = raised.value
    assert error.name == "model_quantized.onnx"
    assert error.expected == digest(MODEL_BYTES)
    assert error.actual == digest(b"truncated-garbage")

    directory = cache / MODEL_NAME
    assert not (directory / "model_quantized.onnx").exists()
    assert not list(directory.glob("*.part")), "the partial download must be cleaned up"
    # The failure stops the run: nothing else was fetched after it.
    assert calls == [expected_url("model_quantized.onnx")]


def test_partial_hit_fetches_only_the_missing_file(cache):
    seed(cache, names=["model_quantized.onnx"])
    calls: list[str] = []

    directory = ensure_model(spec_for(), fetch=recording_fetch(calls))

    assert calls == [expected_url("tokenizer.json")]
    assert (directory / "tokenizer.json").read_bytes() == TOKENIZER_BYTES


def test_existing_file_with_a_wrong_digest_is_refetched_not_trusted(cache):
    directory = seed(cache)
    (directory / "model_quantized.onnx").write_bytes(b"a corrupted weight file")
    calls: list[str] = []

    ensure_model(spec_for(), fetch=recording_fetch(calls))

    assert calls == [expected_url("model_quantized.onnx")]
    assert (directory / "model_quantized.onnx").read_bytes() == MODEL_BYTES


def test_failed_refetch_of_a_corrupt_existing_file_leaves_nothing_pretending_to_be_ready(cache):
    """A corrupt file plus a failed repair must not leave a file under the real name."""
    directory = seed(cache)
    (directory / "model_quantized.onnx").write_bytes(b"a corrupted weight file")

    def corrupted_fetch(url, destination, timeout):
        destination.write_bytes(b"still garbage")

    with pytest.raises(ModelIntegrityError):
        ensure_model(spec_for(), fetch=corrupted_fetch)

    assert not (directory / "model_quantized.onnx").exists()
    assert not list(directory.glob("*.part"))


def test_cache_root_on_windows_is_localappdata(tmp_path, monkeypatch):
    monkeypatch.setattr(sys, "platform", "win32")
    monkeypatch.setenv("LOCALAPPDATA", str(tmp_path / "LocalAppData"))

    assert model_store.cache_root() == tmp_path / "LocalAppData" / "lover-instance" / "models"


def test_cache_root_outside_windows_is_xdg_cache_home(tmp_path, monkeypatch):
    monkeypatch.setattr(sys, "platform", "linux")
    monkeypatch.setenv("XDG_CACHE_HOME", str(tmp_path / "xdg-cache"))

    assert model_store.cache_root() == tmp_path / "xdg-cache" / "lover-instance" / "models"


def test_cache_root_falls_back_to_the_home_cache_directory(tmp_path, monkeypatch):
    monkeypatch.setattr(sys, "platform", "linux")
    monkeypatch.delenv("XDG_CACHE_HOME", raising=False)
    monkeypatch.setenv("HOME", str(tmp_path / "home"))

    assert model_store.cache_root() == tmp_path / "home" / ".cache" / "lover-instance" / "models"


def test_default_fetch_calls_urlopen_with_the_given_timeout_and_writes_the_body(cache, monkeypatch):
    calls: list[tuple[str, float | None]] = []

    def fake_urlopen(url, timeout=None):
        calls.append((url, timeout))
        return io.BytesIO(MODEL_BYTES)

    monkeypatch.setattr(urllib.request, "urlopen", fake_urlopen)
    spec = spec_for({"model_quantized.onnx": MODEL_BYTES})

    directory = ensure_model(spec, timeout=42.0)

    assert calls == [(expected_url("model_quantized.onnx"), 42.0)]
    assert (directory / "model_quantized.onnx").read_bytes() == MODEL_BYTES


def test_default_timeout_is_not_zero():
    """A zero timeout is a hang, not a default: it must never be the shipped value."""
    assert model_store.DEFAULT_TIMEOUT > 0
    assert model_store.DEFAULT_ENDPOINT == "https://hf-mirror.com"


@pytest.mark.parametrize("timeout", [0.0, -1.0])
def test_non_positive_timeout_is_rejected_before_any_download(cache, timeout):
    calls: list[str] = []

    with pytest.raises(ValueError):
        ensure_model(spec_for(), timeout=timeout, fetch=recording_fetch(calls))

    assert calls == []


def test_sources_manifest_records_the_revision_and_the_verified_digests(cache):
    """`SOURCES.json` is the provenance record: which revision produced these bytes."""
    calls: list[str] = []

    directory = ensure_model(spec_for(), fetch=recording_fetch(calls))

    manifest = model_store.read_sources(directory)
    assert manifest == {"revision": REVISION, "files": {name: digest(content) for name, content in CONTENTS.items()}}


def test_a_stale_sources_manifest_never_blocks_a_verified_hit(cache):
    """Hashes decide readiness, not a bookkeeping file: a wrong manifest still hits."""
    directory = seed(cache)
    (directory / "SOURCES.json").write_text('{"revision": "rev-0", "files": {}}', encoding="utf-8")

    ensure_model(spec_for(), fetch=exploding_fetch)

    assert (directory / "model_quantized.onnx").read_bytes() == MODEL_BYTES
