"""M2 Task 3: the model-version whitelist and its lazy loader.

What is pinned here:

- `versions()` is the complete menu `/health` reports and the only set of values
  `/score` accepts besides the dictionary version.
- Every entry names three files by sha256, because a weight file is its digest
  (plan D2): a cache that does not match is not the model that was reviewed.
- `load()` builds an encoder at most once per process, per version. Building it
  twenty times would mean opening a 100 MB session on every block.
- `load()` re-applies the entry's `max_length` and rejects a `positive_index`
  outside the model's output columns. Task 1 recorded the second as a real
  hazard: a negative index does not raise, it silently reads the *other*
  column, which reverses the sign of every score in the report.

Nothing here downloads a file or opens a session: `ensure_model` and
`Encoder.from_model_dir` are both replaced.
"""

import pathlib

import pytest

from app import model_registry
from app.model_registry import ModelEntry
from app.model_store import ModelSpec

ERLANGSHEN = "erlangshen-110m-v1"
DISTILBERT = "distilbert-multilingual-v1"


class _FakeTokenizer:
    def __init__(self) -> None:
        self.truncation: int | None = None

    def enable_truncation(self, max_length: int) -> None:
        self.truncation = max_length


class _FakeEncoder:
    def __init__(self, labels=("Negative", "Positive"), max_length: int = 128):
        self.labels = list(labels)
        self.max_length = max_length
        self.tokenizer = _FakeTokenizer()


@pytest.fixture(autouse=True)
def _clear_encoder_cache():
    """The cache is process state; a leaked encoder would hide a broken load."""
    model_registry._CACHE.clear()
    yield
    model_registry._CACHE.clear()


def _stub_build(monkeypatch, encoder: _FakeEncoder | None = None) -> list[pathlib.Path]:
    """Replace the download and the session build; return the directories passed in."""
    directories: list[pathlib.Path] = []
    built = encoder or _FakeEncoder()

    monkeypatch.setattr(
        model_registry,
        "ensure_model",
        lambda spec, **kwargs: pathlib.Path("C:/fake/models") / spec.name,
    )

    def from_model_dir(cls, directory, model_file: str = "model_quantized.onnx"):
        directories.append(pathlib.Path(directory))
        return built

    monkeypatch.setattr(model_registry.Encoder, "from_model_dir", classmethod(from_model_dir))
    return directories


def test_versions_are_exactly_the_two_pinned_candidates():
    assert model_registry.versions() == [ERLANGSHEN, DISTILBERT]


def test_get_returns_none_for_an_unknown_version():
    assert model_registry.get("gpt-9-turbo") is None


def test_every_entry_pins_three_files_by_sha256():
    for version in model_registry.versions():
        entry = model_registry.get(version)
        assert entry.version == version
        assert isinstance(entry.spec, ModelSpec)
        assert set(entry.spec.files) == {"config.json", "model_quantized.onnx", "tokenizer.json"}
        for name, digest in entry.spec.files.items():
            assert len(digest) == 64, f"{version}/{name} is not a sha256"
            assert digest == digest.lower(), f"{version}/{name} is not lowercase hex"


def test_binary_candidate_carries_a_tau_and_the_three_class_one_does_not():
    """`tau = 0.0` is how a three-class model says "I have a neutral column"."""
    erlangshen = model_registry.get(ERLANGSHEN)
    assert erlangshen.positive_index == 1, "id2label is {0:Negative, 1:Positive}"
    assert erlangshen.tau == 0.45

    distilbert = model_registry.get(DISTILBERT)
    assert distilbert.positive_index == 0, "id2label is {0:positive, 1:neutral, 2:negative}"
    assert distilbert.tau == 0.0


def test_load_builds_each_version_once(monkeypatch):
    directories = _stub_build(monkeypatch)

    first = model_registry.load(ERLANGSHEN)
    second = model_registry.load(ERLANGSHEN)

    assert first is second
    assert len(directories) == 1
    assert directories[0].name == "erlangshen-110m-sentiment"


def _three_files() -> dict[str, str]:
    """A spec that pins the three real file names. Nothing downloads it here.

    `files={}` used to stand in for "some spec", which quietly blessed a config
    that would make `ensure_model` return an empty directory and `load` report a
    working model. `load` now refuses it, so fixtures spell out the real names.
    """
    return {"config.json": "a" * 64, "model_quantized.onnx": "b" * 64, "tokenizer.json": "c" * 64}


def test_load_applies_the_entry_max_length(monkeypatch):
    _stub_build(monkeypatch)
    monkeypatch.setitem(
        model_registry.MODELS,
        "tiny-v1",
        ModelEntry(
            version="tiny-v1",
            spec=ModelSpec(name="tiny", revision="main", files=_three_files()),
            positive_index=1,
            tau=0.25,
            max_length=64,
        ),
    )

    encoder = model_registry.load("tiny-v1")

    assert encoder.max_length == 64
    assert encoder.tokenizer.truncation == 64


def test_load_rejects_a_positive_index_outside_the_output_columns(monkeypatch):
    """A negative index reads the other column silently; Task 1 flagged this."""
    _stub_build(monkeypatch)
    monkeypatch.setitem(
        model_registry.MODELS,
        "bad-v1",
        ModelEntry(
            version="bad-v1",
            spec=ModelSpec(name="bad", revision="main", files=_three_files()),
            positive_index=-1,
            tau=0.25,
        ),
    )

    with pytest.raises(ValueError, match="positive_index"):
        model_registry.load("bad-v1")


def test_load_rejects_an_empty_spec_without_downloading(monkeypatch):
    """An empty `files` map is not "no files needed", it is a broken pin.

    `ensure_model` would satisfy it by returning an empty directory (fail-open),
    and the failure would then surface as an onnxruntime error about a missing
    `config.json`. Refusing here keeps the message at the configuration that is
    actually wrong — and after Task 3's narrowed `except`, this reaches the caller
    as a 500 instead of a retryable 503.
    """
    downloads: list[ModelSpec] = []
    monkeypatch.setattr(
        model_registry, "ensure_model", lambda spec, **kwargs: downloads.append(spec)
    )
    monkeypatch.setitem(
        model_registry.MODELS,
        "empty-v1",
        ModelEntry(
            version="empty-v1",
            spec=ModelSpec(name="empty", revision="main", files={}),
            positive_index=1,
            tau=0.25,
        ),
    )

    with pytest.raises(ValueError, match="files"):
        model_registry.load("empty-v1")

    assert downloads == [], "an empty spec must be refused before any download"


def test_load_unknown_version_raises_instead_of_downloading(monkeypatch):
    directories = _stub_build(monkeypatch)

    with pytest.raises(KeyError):
        model_registry.load("gpt-9-turbo")

    assert directories == []
