"""Fetch ONNX weights once, verify them by pinned sha256, then run offline.

M2 rule D2: a file is named by its digest, not by a URL. `spec.files` maps each
file name to the sha256 it must have, so the cache on disk is either exactly the
weights that were reviewed or nothing at all.

Four rules cost real bugs if broken:

1. **A complete cache makes no request.** Readiness is decided by hashing what is
   already on disk, before any URL is built. Restarting the service must not
   depend on hf-mirror being reachable.
2. **Content becomes a weight only after its digest matches.** Downloads go to
   `<file>.part` and are promoted with `os.replace`. Without that, a dropped
   connection leaves a file that *looks* ready and fails inside onnxruntime with
   an error that says nothing about a bad download.
3. **An existing file with the wrong digest is mismatched, not present.** The
   common failure is a truncated first download; treating "the name is there" as
   a hit is how that survives a reboot.
4. **The timeout is always positive.** `urlopen(timeout=0)` raises immediately and
   `None` waits forever; neither is a default anyone can live with, so
   `DEFAULT_TIMEOUT` is explicit and a non-positive value is rejected up front.

`SOURCES.json` in the target directory records which revision produced the bytes.
It is written only after a download has been verified, and it is never read on
the hit path: the digests in `spec.files` are the authority, so a stale or
hand-edited manifest cannot demote a verified model (`read_sources` exists for
the report and for humans, not for the readiness decision).

`fetch` is the one injection point: tests hand in a fake that writes files, and
the default implementation is `urllib.request`. Nothing here reads the network
configuration of the machine; the endpoint is a parameter with a constant
default, and only `cache_root` reads the environment.
"""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import shutil
import sys
import urllib.request
from dataclasses import dataclass
from typing import Callable

# hf-mirror, not huggingface.co: measured unreachable from this machine.
DEFAULT_ENDPOINT = "https://hf-mirror.com"

# Seconds. Long enough for a 100 MB weight over a slow link, and never zero.
DEFAULT_TIMEOUT = 60.0

MANIFEST_NAME = "SOURCES.json"

_PART_SUFFIX = ".part"
_CHUNK_SIZE = 1 << 20

# `(url, destination, timeout)` — downloaded bytes must land in `destination`.
Fetch = Callable[[str, pathlib.Path, float], None]


@dataclass(frozen=True)
class ModelSpec:
    """A pinned model: where it comes from (`revision`) and what it must be.

    `files` maps file name (relative to the model directory) to its lowercase
    sha256. Changing a file's hash is the same act as changing the model, which
    is why it lives next to `revision` in the same immutable record.
    """

    name: str
    revision: str
    files: dict[str, str]


class ModelIntegrityError(RuntimeError):
    """Downloaded bytes did not match the pinned digest; nothing was installed."""

    def __init__(self, name: str, expected: str, actual: str):
        super().__init__(f"{name}: sha256 mismatch (expected {expected}, got {actual})")
        self.name = name
        self.expected = expected
        self.actual = actual


def cache_root() -> pathlib.Path:
    """Where models live on this machine.

    Windows keeps caches in `%LOCALAPPDATA%`; elsewhere the XDG cache home wins,
    with `~/.cache` as the fallback for shells that do not set it. `HOME` is read
    explicitly before `expanduser`, so the fallback is testable and does not
    depend on how the platform resolves `~`.
    """
    if sys.platform == "win32":
        # `LOCALAPPDATA` is set for every interactive Windows session; the
        # profile path is only a guard for service accounts without a profile.
        base = os.environ.get("LOCALAPPDATA") or os.path.join(
            os.path.expanduser("~"), "AppData", "Local"
        )
        return pathlib.Path(base) / "lover-instance" / "models"

    base = os.environ.get("XDG_CACHE_HOME")
    if base:
        return pathlib.Path(base) / "lover-instance" / "models"
    home = os.environ.get("HOME") or os.path.expanduser("~")
    return pathlib.Path(home) / ".cache" / "lover-instance" / "models"


def sha256_file(path: pathlib.Path) -> str:
    """Lowercase hex digest of a file, read in chunks (weights are ~100 MB)."""
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(_CHUNK_SIZE), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_sources(directory: pathlib.Path) -> dict:
    """The recorded provenance of a model directory, or `{}` if absent.

    Informational only: readiness is decided by rehashing the weights, never by
    this file.
    """
    path = pathlib.Path(directory) / MANIFEST_NAME
    if not path.is_file():
        return {}
    return json.loads(path.read_text(encoding="utf-8"))


def _write_sources(directory: pathlib.Path, spec: ModelSpec) -> None:
    payload = {
        "revision": spec.revision,
        "files": {name: digest.lower() for name, digest in spec.files.items()},
    }
    (directory / MANIFEST_NAME).write_text(
        json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )


def _file_url(endpoint: str, spec: ModelSpec, name: str) -> str:
    """The hf-mirror resolve URL for one file of a pinned revision."""
    return f"{endpoint.rstrip('/')}/{spec.name}/resolve/{spec.revision}/{name}"


def _download(url: str, destination: pathlib.Path, timeout: float) -> None:
    """Default `fetch`: stream `url` into `destination`, with an explicit timeout."""
    with urllib.request.urlopen(url, timeout=timeout) as response, open(destination, "wb") as handle:
        shutil.copyfileobj(response, handle)


def ensure_model(
    spec: ModelSpec,
    *,
    endpoint: str = DEFAULT_ENDPOINT,
    timeout: float = DEFAULT_TIMEOUT,
    fetch: Fetch | None = None,
) -> pathlib.Path:
    """Path to a local directory holding exactly `spec`, downloading only what is missing.

    Happy path: every pinned file is already there with the right digest, so the
    function returns without touching the network. Otherwise each missing or
    mismatched file is fetched to `<file>.part`, verified, and only then renamed.
    A digest mismatch raises `ModelIntegrityError` and leaves no file under the
    final name, so the next start retries instead of running on garbage.
    """
    if timeout is None or timeout <= 0:
        raise ValueError(f"timeout must be positive, got {timeout!r}: a zero timeout is a hang, not a default")

    directory = cache_root() / spec.name
    directory.mkdir(parents=True, exist_ok=True)

    missing: list[tuple[str, str]] = []
    for name, digest in spec.files.items():
        target = directory / name
        expected = digest.strip().lower()
        if target.is_file() and sha256_file(target) == expected:
            continue
        missing.append((name, expected))

    if not missing:
        return directory

    fetcher: Fetch = fetch or _download
    for name, expected in missing:
        target = directory / name
        target.parent.mkdir(parents=True, exist_ok=True)
        part = target.with_name(target.name + _PART_SUFFIX)
        # Drop the known-bad copy first: an interrupted repair must not leave a
        # file wearing the final name that the next run would hash all over again.
        target.unlink(missing_ok=True)
        try:
            fetcher(_file_url(endpoint, spec, name), part, timeout)
            actual = sha256_file(part)
            if actual != expected:
                raise ModelIntegrityError(name, expected, actual)
            os.replace(part, target)
        except BaseException:
            part.unlink(missing_ok=True)
            raise

    _write_sources(directory, spec)
    return directory
