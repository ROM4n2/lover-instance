"""Task 7 RED: batched score contract.

Contract pinned here:
- request  {model_version, items:[{msg_id, content}]}, at most 256 items
- response {model_version, results:[{msg_id, polarity, emotion, intensity, tokens}]}
- NO identity fields in the request (no is_self / sender / talker) and NO
  `top_words` in the response: splitting self vs partner is Go's job, because
  only Go knows `is_self`
- `score_batch` is a pure function: identical input, byte-identical output
- over the limit -> 413 with an explicit `code`; empty batch -> 200 + []
"""

import json

from fastapi.testclient import TestClient

from app.main import create_app
from app.score import MAX_ITEMS, score_batch

client = TestClient(create_app())


def _batch(size: int) -> list[dict]:
    return [{"msg_id": 1000 + i, "content": f"今天特别开心[捂脸] 第{i}条"} for i in range(size)]


def test_normal_batch_returns_one_result_per_item_in_order():
    items = _batch(3)
    resp = client.post("/score", json={"model_version": "lexicon-hownet-v1", "items": items})
    assert resp.status_code == 200
    body = resp.json()
    assert body["model_version"] == "lexicon-hownet-v1"
    assert len(body["results"]) == len(items)
    assert [r["msg_id"] for r in body["results"]] == [i["msg_id"] for i in items]


def test_every_result_carries_tokens_and_keeps_expression_mark_verbatim():
    resp = client.post("/score", json={"model_version": "v", "items": _batch(1)})
    result = resp.json()["results"][0]
    assert isinstance(result["tokens"], list)
    assert all(isinstance(token, str) for token in result["tokens"])
    assert "[捂脸]" in result["tokens"]


def test_result_scores_are_typed_and_bounded():
    result = client.post("/score", json={"model_version": "v", "items": _batch(1)}).json()["results"][0]
    assert -1.0 <= result["polarity"] <= 1.0
    assert 0.0 <= result["intensity"] <= 1.0
    assert isinstance(result["emotion"], str)


def test_too_many_items_is_413_with_code():
    resp = client.post("/score", json={"model_version": "v", "items": _batch(MAX_ITEMS + 1)})
    assert resp.status_code == 413
    assert "code" in resp.json()


def test_oversized_body_is_413_with_code():
    huge = [{"msg_id": i, "content": "a" * 25_000} for i in range(256)]
    resp = client.post("/score", json={"model_version": "v", "items": huge})
    assert resp.status_code == 413
    assert "code" in resp.json()


def test_empty_items_is_200_with_empty_results():
    resp = client.post("/score", json={"model_version": "v", "items": []})
    assert resp.status_code == 200
    body = resp.json()
    assert body["results"] == []
    assert body["model_version"] == "v"


def test_score_batch_is_pure():
    items = _batch(5)
    first = json.dumps(score_batch(items), ensure_ascii=False, sort_keys=True)
    second = json.dumps(score_batch(items), ensure_ascii=False, sort_keys=True)
    assert first == second


def test_contract_carries_no_identity_and_no_top_words():
    body = client.post("/score", json={"model_version": "v", "items": _batch(2)}).json()
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
        json={"model_version": "v", "items": [{"msg_id": 1, "content": "开心", "is_self": True}]},
    )
    assert resp.status_code == 422