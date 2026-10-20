"""The model table the `lover` CLI reads, and the one operation that downloads.

WHY THIS MODULE EXISTS
----------------------
`lover model list/add/verify/rm` is a Go command, but every fact it needs lives
here: the whitelist (`model_registry.MODELS`), the pinned digests
(`ModelSpec.files`) and the cache location (`model_store.cache_root`). Copying
any of those into Go would create a second source of truth that drifts the first
time a model is added or a pin changes — the same "one rule, two
implementations" defect that the shared `internal/app` package exists to prevent
on the Go side. So the CLI asks this module instead of guessing, and the version
table has exactly one home.

WHY A SUBPROCESS AND NOT `GET /api/models`
------------------------------------------
A read-only HTTP endpoint on the service is the tidier shape, and it was the
recommended one. It was not taken, for one reason: `lover model` has to work when
no service is running. `list` is a menu, `rm` deletes files, and `add` is the
step that makes a *later* run able to score with a model — all of them happen
before the first `lover` ever starts uvicorn, and on a machine where port 8000 is
held by something else a listing would fail with "service unhealthy" for a
question that has nothing to do with the service. This module is the same
registry the service reads, reached without a socket.

This is not text parsing of `model_registry.py` either: it imports the module, so
a new entry or a changed pin is visible here the moment it is committed.

WHAT THE TWO SUBCOMMANDS DO
---------------------------
- `registry` prints the version table and the cache root as JSON. It imports
  `model_registry`, which imports `onnx_encoder` (numpy, nothing heavier), so it
  loads no weights and makes no request: `/health`'s "a probe must be cheap" rule
  holds here too.
- `ensure <version>` runs `model_store.ensure_model` — the only code in this
  repository that writes to the model cache. It downloads only what is missing or
  mismatched, verifies every byte by sha256, and promotes it from `<file>.part`,
  so a dropped connection can never leave a file that looks ready. It is reached
  only through the explicit `lover model add`, never from a listing or a probe.

Deleting a cache directory is deliberately NOT here: `rm` is a filesystem
operation on a path this module reports, and the Go command owns the
confirmation (`--yes`) and the removal, so the "what will be deleted" question is
answered by the process that does the deleting.

Privacy: nothing here reads a message, a talker or a conversation key. The output
is version names, file names, paths and digests.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from app import model_registry, model_store


def registry_payload() -> dict:
    """The version table and the cache root, as JSON-ready data.

    Read-only by construction: the only call is `cache_root()`, which reads the
    environment. `MODELS` is a module-level dict built at import time.
    """
    return {
        "cache_root": str(model_store.cache_root()),
        "models": [
            {
                "version": entry.version,
                "name": entry.spec.name,
                "revision": entry.spec.revision,
                # Lower-cased here so the CLI compares like with like; the pin
                # is already lowercase in the registry, and a digest comparison
                # that differs only in case is a bug nobody would find.
                "files": {
                    name: digest.strip().lower() for name, digest in entry.spec.files.items()
                },
            }
            for entry in model_registry.MODELS.values()
        ],
    }


def _already_verified(directory: Path, name: str, expected: str) -> bool:
    """Whether `directory/name` is exactly the pinned bytes.

    Used for one purpose only: telling the user which files this call had to
    fetch. The readiness decision is `ensure_model`'s, made again after the
    download; this is a report, not a gate, and it must never be read as one.
    """
    target = directory / name
    return target.is_file() and model_store.sha256_file(target) == expected


def ensure_payload(version: str) -> dict:
    """Download and verify one model version, then report what it took.

    Raises `KeyError` for a version that is not configured — the caller turns it
    into the usage error it is — and lets `model_store`'s `ModelIntegrityError`
    through unchanged: it already says which file, which pin and which digest,
    and it leaves no file under the final name.
    """
    entry = model_registry.MODELS[version]
    directory = model_store.cache_root() / entry.spec.name

    missing = [
        name
        for name, digest in entry.spec.files.items()
        if not _already_verified(directory, name, digest.strip().lower())
    ]

    installed = model_store.ensure_model(entry.spec)

    return {
        "version": version,
        "path": str(installed),
        "fetched": [name for name in missing if (installed / name).is_file()],
        "files": [
            {"name": name, "sha256": model_store.sha256_file(installed / name)}
            for name in entry.spec.files
        ],
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="python -m app.model_cli",
        description="Read the model registry, or fetch and verify one model version.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    sub.add_parser(
        "registry",
        help="print the version table and the cache root as JSON; downloads nothing",
    )
    ensure = sub.add_parser(
        "ensure",
        help="download and verify one model version, then print its path and digests",
    )
    ensure.add_argument("version", help="a version from the registry, e.g. erlangshen-110m-v1")

    args = parser.parse_args(argv)

    if args.command == "registry":
        print(json.dumps(registry_payload()))
        return 0

    try:
        print(json.dumps(ensure_payload(args.version)))
    except KeyError:
        # An unknown version is a command-line mistake, not a service failure:
        # name the menu so the fix is a copy-paste.
        print(
            f"unknown model version {args.version!r}; configured: {', '.join(model_registry.MODELS)}",
            file=sys.stderr,
        )
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
