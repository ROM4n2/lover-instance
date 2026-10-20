"""Task 7 RED: batched score contract.

Contract pinned here:
- request  {model_version, items:[{msg_id, content}]}, at most 256 items
- response {model_version, results:[{msg_id, polarity, emotion, intensity, tokens}]}
- NO identity fields in the request (no is_self / sender / talker) and NO
  `top_words` in the response: splitting self vs partner is Go's job, because
  only Go knows `is_self`
- `score_batch` is a pure function: identical input, byte-identical output
- over the limit -> 413 with an explicit `code`; empty batch -> 200 + []

M2 Task 3 adds version selection:

- `model_version=""` means "whatever this service scores with by default" and
  stays the dictionary baseline. Anything else has to be a version the registry
  knows, or the request is refused: a score is filed under `model_version` in
  the report, so scoring an unknown version with the dictionary would credit the
  dictionary's guesses to a model that never ran (ADR-0004).
- A model version answers with `emotion == ""`. Neither candidate predicts DUTIR
  emotion categories, and inventing one would turn a 2-class polarity into a
  claim about what the partner felt.
- Weights that cannot be fetched or loaded are 503, not 400 and not 500: the
  request was valid, the service is not ready.

Nothing in this file touches the network or a real weight file: `model_registry.load`
is replaced with a double that returns canned probabilities.
"""

import json
import types

import pytest
from fastapi.testclient import TestClient

from app import model_registry
from app.main import create_app
from app.model_store import ModelIntegrityError, ModelSpec
from app.score import MAX_ITEMS, score_batch
from app.sentiment_lexicon import MODEL_VERSION
from app.tokenize import countable_tokens

client = TestClient(create_app())

ERLANGSHEN = "erlangshen-110m-v1"
DISTILBERT = "distilbert-multilingual-v1"


def _three_files() -> dict[str, str]:
    """A spec that pins the three real file names; nothing is downloaded here.

    An empty `files` map is not a legal spec — `model_registry.load` now refuses
    it — so fixtures that only need *a* spec still spell out what one holds.
    """
    return {"config.json": "a" * 64, "model_quantized.onnx": "b" * 64, "tokenizer.json": "c" * 64}


def _batch(size: int) -> list[dict]:
    return [{"msg_id": 1000 + i, "content": f"今天特别开心[捂脸] 第{i}条"} for i in range(size)]


class FakeEncoder:
    """The slice of `Encoder` the scoring path may use: labels and probabilities.

    Rows are looked up by text, so a blank item that leaked into the batch raises
    `KeyError` instead of silently shifting every probability by one row — that
    misalignment would mislabel every later message in the block.
    """

    def __init__(self, by_text: dict[str, list[float]], labels: list[str]):
        self.by_text = by_text
        self.labels = list(labels)
        self.batches: list[list[str]] = []

    def probabilities(self, texts: list[str]) -> list[list[float]]:
        self.batches.append(list(texts))
        return [self.by_text[text] for text in texts]


def _stub_load(monkeypatch, encoder: FakeEncoder) -> list[str]:
    """Replace `model_registry.load`; return the list of versions it was asked for."""
    calls: list[str] = []

    def load(version: str) -> FakeEncoder:
        calls.append(version)
        return encoder

    monkeypatch.setattr(model_registry, "load", load)
    return calls


def test_normal_batch_returns_one_result_per_item_in_order():
    items = _batch(3)
    resp = client.post("/score", json={"model_version": "lexicon-hownet-v1", "items": items})
    assert resp.status_code == 200
    body = resp.json()
    assert body["model_version"] == "lexicon-hownet-v1"
    assert len(body["results"]) == len(items)
    assert [r["msg_id"] for r in body["results"]] == [i["msg_id"] for i in items]


def test_every_result_carries_tokens_and_keeps_expression_mark_verbatim():
    resp = client.post("/score", json={"model_version": MODEL_VERSION, "items": _batch(1)})
    result = resp.json()["results"][0]
    assert isinstance(result["tokens"], list)
    assert all(isinstance(token, str) for token in result["tokens"])
    assert "[捂脸]" in result["tokens"]


def test_result_scores_are_typed_and_bounded():
    result = client.post("/score", json={"model_version": MODEL_VERSION, "items": _batch(1)}).json()[
        "results"
    ][0]
    assert -1.0 <= result["polarity"] <= 1.0
    assert 0.0 <= result["intensity"] <= 1.0
    assert isinstance(result["emotion"], str)


def test_too_many_items_is_413_with_code():
    resp = client.post("/score", json={"model_version": MODEL_VERSION, "items": _batch(MAX_ITEMS + 1)})
    assert resp.status_code == 413
    assert "code" in resp.json()


def test_oversized_body_is_413_with_code():
    huge = [{"msg_id": i, "content": "a" * 25_000} for i in range(256)]
    resp = client.post("/score", json={"model_version": MODEL_VERSION, "items": huge})
    assert resp.status_code == 413
    assert "code" in resp.json()


def test_empty_items_is_200_with_empty_results():
    resp = client.post("/score", json={"model_version": MODEL_VERSION, "items": []})
    assert resp.status_code == 200
    body = resp.json()
    assert body["results"] == []
    assert body["model_version"] == MODEL_VERSION


def test_score_batch_is_pure():
    items = _batch(5)
    first = json.dumps(score_batch(items), ensure_ascii=False, sort_keys=True)
    second = json.dumps(score_batch(items), ensure_ascii=False, sort_keys=True)
    assert first == second


def test_contract_carries_no_identity_and_no_top_words():
    body = client.post("/score", json={"model_version": MODEL_VERSION, "items": _batch(2)}).json()
    assert set(body.keys()) == {"model_version", "results"}
    for result in body["results"]:
        assert set(result.keys()) == {"msg_id", "polarity", "emotion", "intensity", "tokens"}
    serialized = json.dumps(body)
    for forbidden in ("is_self", "sender", "talker", "nickname", "wxid", "top_words"):
        assert forbidden not in serialized


def test_identity_field_in_request_is_rejected():
    """Go must not be able to smuggle identity in; the schema forbids extras."""
    resp = client.post(
        "/score",
        json={
            "model_version": MODEL_VERSION,
            "items": [{"msg_id": 1, "content": "开心", "is_self": True}],
        },
    )
    assert resp.status_code == 422


# --- M2 Task 3: version selection -------------------------------------------


def test_unknown_model_version_is_400_and_never_scores_with_the_lexicon(monkeypatch):
    """The direct guard against a silent fallback to the dictionary.

    `calls` must stay empty: reaching the lexicon here is exactly the bug this
    test exists to catch, because the response would look perfectly normal.
    """
    import app.score as score_module

    calls: list[str] = []

    def lexicon_stub(text: str) -> types.SimpleNamespace:
        calls.append(text)
        return types.SimpleNamespace(polarity=0.0, emotion="neutral", intensity=0.0)

    monkeypatch.setattr(score_module, "score", lexicon_stub)

    resp = client.post("/score", json={"model_version": "gpt-9-turbo", "items": _batch(2)})

    assert resp.status_code == 400
    body = resp.json()
    assert body["code"] == "unknown_model_version"
    assert "gpt-9-turbo" in body["detail"]
    assert calls == [], "an unknown version fell back to the dictionary"
    assert "今天特别开心" not in resp.text, "errors must not echo chat content"


def test_empty_model_version_scores_with_the_dictionary_default():
    resp = client.post("/score", json={"model_version": "", "items": _batch(2)})
    assert resp.status_code == 200
    body = resp.json()
    assert body["model_version"] == MODEL_VERSION
    assert [r["msg_id"] for r in body["results"]] == [i["msg_id"] for i in _batch(2)]


def test_binary_model_version_uses_the_tau_band_and_leaves_emotion_empty(monkeypatch):
    """Three rows, one per branch: confident positive, confident negative, band.

    Erlangshen's tau is 0.45, so only p > 0.95 and p < 0.05 speak; 0.60 is
    inside the band and must come back as exactly 0.0, not as a weak positive.
    """
    texts = {
        "今天特别开心": [0.02, 0.98],
        "好难过": [0.97, 0.03],
        "明天几点": [0.40, 0.60],
    }
    encoder = FakeEncoder(texts, ["Negative", "Positive"])
    load_calls = _stub_load(monkeypatch, encoder)
    items = [{"msg_id": index + 1, "content": text} for index, text in enumerate(texts)]

    resp = client.post("/score", json={"model_version": ERLANGSHEN, "items": items})

    assert resp.status_code == 200
    body = resp.json()
    assert body["model_version"] == ERLANGSHEN
    assert load_calls == [ERLANGSHEN]
    results = body["results"]
    assert [r["msg_id"] for r in results] == [1, 2, 3]
    assert [r["emotion"] for r in results] == ["", "", ""]
    assert [r["polarity"] for r in results] == pytest.approx([0.98, -0.97, 0.0])
    assert [r["intensity"] for r in results] == pytest.approx([0.98, 0.97, 0.0])
    for result, item in zip(results, items):
        assert result["tokens"] == countable_tokens(item["content"])


def test_three_class_model_version_maps_argmax_to_a_signed_polarity(monkeypatch):
    texts = {
        "太可爱了吧": [0.90, 0.05, 0.05],
        "你真的很讨厌": [0.05, 0.05, 0.90],
        "随便你吧": [0.10, 0.80, 0.10],
    }
    encoder = FakeEncoder(texts, ["positive", "neutral", "negative"])
    _stub_load(monkeypatch, encoder)
    items = [{"msg_id": index + 1, "content": text} for index, text in enumerate(texts)]

    results = client.post("/score", json={"model_version": DISTILBERT, "items": items}).json()["results"]

    assert [r["polarity"] for r in results] == pytest.approx([0.90, -0.90, 0.0])
    assert [r["intensity"] for r in results] == pytest.approx([0.90, 0.90, 0.0])
    assert [r["emotion"] for r in results] == ["", "", ""]


def test_blank_text_on_the_model_path_is_scored_without_inference(monkeypatch):
    """A blank message stays "no opinion" and must not be handed to the model.

    The double raises `KeyError` on any text it was not given a row for, so this
    also proves no blank ever entered the batch.
    """
    encoder = FakeEncoder({"今天特别开心": [0.02, 0.98]}, ["Negative", "Positive"])
    _stub_load(monkeypatch, encoder)
    items = [
        {"msg_id": 1, "content": ""},
        {"msg_id": 2, "content": "今天特别开心"},
        {"msg_id": 3, "content": "   "},
    ]

    results = client.post("/score", json={"model_version": ERLANGSHEN, "items": items}).json()["results"]

    assert encoder.batches == [["今天特别开心"]]
    assert [r["polarity"] for r in results] == pytest.approx([0.0, 0.98, 0.0])
    assert [r["intensity"] for r in results] == pytest.approx([0.0, 0.98, 0.0])
    assert results[0]["tokens"] == []
    assert results[0]["emotion"] == ""


@pytest.mark.parametrize(
    "error",
    [
        ModelIntegrityError("model_quantized.onnx", "a" * 64, "b" * 64),
        OSError("connection reset by peer"),
    ],
)
def test_unavailable_weights_are_503_not_400_and_not_500(monkeypatch, error):
    """The request was valid; the service just cannot serve it yet."""

    def load(version: str):
        raise error

    monkeypatch.setattr(model_registry, "load", load)

    resp = client.post("/score", json={"model_version": ERLANGSHEN, "items": _batch(2)})

    assert resp.status_code == 503
    assert resp.json()["code"] == "model_unavailable"
    assert "今天特别开心" not in resp.text


def test_permanent_load_failures_are_not_the_retryable_503(monkeypatch):
    """A misconfiguration is 500, not 503.

    Go retries every 5xx three times and reads only `code`, so a permanent
    failure dressed as `model_unavailable` costs three downloads and reports
    nothing: the one thing an operator needs is the traceback a 500 leaves in
    the service log. The empty spec is the concrete case the plan named for this
    task (see `model_registry.load`), and `KeyError` is the other one the old
    `except Exception` swallowed.
    """
    permanent = [
        ValueError("empty-v1: spec pins no files"),
        KeyError("no-such-version"),
    ]
    no_rethrow = TestClient(create_app(), raise_server_exceptions=False)

    for error in permanent:
        def load(version: str, _error=error):
            raise _error

        monkeypatch.setattr(model_registry, "load", load)
        resp = no_rethrow.post("/score", json={"model_version": ERLANGSHEN, "items": _batch(2)})

        assert resp.status_code == 500, f"{type(error).__name__} was dressed up as retryable"
        assert "model_unavailable" not in resp.text
        assert "今天特别开心" not in resp.text


def test_the_band_boundary_itself_still_states_an_opinion():
    """`p == 0.5 ± tau` speaks, and both sides read it the same way.

    `apply_neutral_band` has always been strict (`|p - 0.5| < tau`), and Task 1's
    mutation tests pin it that way: the band is the width of "no opinion", not a
    threshold to round. `_polarity` had drifted to `p > 0.5 + tau` / `p < 0.5 -
    tau`, which turns the exact boundary into a shrug; tau = 0.5 keeps the
    arithmetic exact so the test is about the comparison, not about rounding.
    """
    from app.score import _polarity

    entry = model_registry.ModelEntry(
        version="boundary-v1",
        spec=ModelSpec(name="boundary", revision="main", files=_three_files()),
        positive_index=1,
        tau=0.5,
    )
    labels = ["Negative", "Positive"]

    high = _polarity([0.5, 0.5 + entry.tau], labels, entry)
    low = _polarity([0.5, 0.5 - entry.tau], labels, entry)

    assert high == pytest.approx(1.0), "p == 0.5 + tau came back neutral"
    assert low == pytest.approx(-1.0), "p == 0.5 - tau came back neutral"


def test_three_class_model_without_a_negative_label_refuses_to_guess():
    """No `neg*` column means the argmax cannot be signed; say so.

    Returning 0.0 there would file a confident negative prediction as "no
    opinion", which is unlabelled substitution in the other direction.
    """
    from app.score import _polarity

    entry = model_registry.ModelEntry(
        version="nameless-v1",
        spec=ModelSpec(name="nameless", revision="main", files=_three_files()),
        positive_index=0,
        tau=0.0,
    )

    with pytest.raises(RuntimeError, match="negative"):
        _polarity([0.1, 0.1, 0.8], ["positive", "neutral", "sad"], entry)


def test_limits_still_apply_on_the_model_path(monkeypatch):
    encoder = FakeEncoder({}, ["Negative", "Positive"])
    load_calls = _stub_load(monkeypatch, encoder)

    too_many = client.post(
        "/score", json={"model_version": ERLANGSHEN, "items": _batch(MAX_ITEMS + 1)}
    )
    huge = client.post(
        "/score",
        json={
            "model_version": ERLANGSHEN,
            "items": [{"msg_id": i, "content": "a" * 25_000} for i in range(256)],
        },
    )

    assert too_many.status_code == 413
    assert huge.status_code == 413
    assert encoder.batches == [], "an over-limit batch must be refused before inference"
    assert load_calls == [], "an over-limit batch must not download or build a model"


def test_score_batch_refuses_an_unknown_version_without_falling_back():
    """The guard holds below the HTTP layer too: Go calls the function in tests."""
    from app.score import UnknownModelVersion

    with pytest.raises(UnknownModelVersion):
        score_batch([{"msg_id": 1, "content": "开心"}], "no-such-version")
