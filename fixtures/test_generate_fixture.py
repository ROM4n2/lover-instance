"""Task 1 合成语料 fixture 生成器 — failing-first TDD suite (RED first)."""
import json
from collections import Counter

import generate_fixture as gf

REQUIRED_FIELDS = ("seq", "time", "talker", "sender", "isSelf", "type", "subType", "content")
REQUIRED_FLAGS = ("撤回消息", "系统消息", "红包", "图片语音占位", "自定义表情", "时间戳混用", "深夜消息", "同秒连发")


def _to_ms(value):
    """Normalize a mixed s/ms/µs timestamp to epoch milliseconds."""
    if value < 100_000_000_000:
        return value * 1000
    if value < 100_000_000_000_000:
        return value
    return value // 1000


def _generate(tmp_path, seed=42):
    out_path = tmp_path / "sample_chat.json"
    gf.generate_chat(out_path=out_path, seed=seed)
    return json.loads(out_path.read_text(encoding="utf-8"))


def test_every_message_has_exactly_the_required_fields(tmp_path):
    messages = _generate(tmp_path)
    assert messages, "corpus must not be empty"
    for message in messages:
        assert set(message.keys()) == set(REQUIRED_FIELDS), message


def test_fifty_messages_share_one_identical_millisecond(tmp_path):
    messages = _generate(tmp_path)
    raw_counts = Counter(m["time"] for m in messages)
    ms_counts = Counter(_to_ms(m["time"]) for m in messages)
    second_counts = Counter(_to_ms(m["time"]) // 1000 for m in messages)
    assert max(raw_counts.values()) == 50, "50 messages must share one raw timestamp value"
    assert max(ms_counts.values()) == 50, "50 messages must share one normalized ms timestamp"
    assert max(second_counts.values()) == 50, "50 messages must land in one second"


def test_seed_42_output_is_byte_identical_across_runs(tmp_path):
    first = tmp_path / "run_one.json"
    second = tmp_path / "run_two.json"
    gf.generate_chat(out_path=first, seed=42)
    gf.generate_chat(out_path=second, seed=42)
    assert first.read_bytes(), "artifact must be non-empty"
    assert first.read_bytes() == second.read_bytes(), "seed=42 must be byte-stable"


def test_different_seed_produces_different_bytes(tmp_path):
    default_run = tmp_path / "seed_42.json"
    other_run = tmp_path / "seed_43.json"
    gf.generate_chat(out_path=default_run, seed=42)
    gf.generate_chat(out_path=other_run, seed=43)
    assert default_run.read_bytes() != other_run.read_bytes(), "seed must actually drive generation"


def test_all_eight_scenario_markers_present(tmp_path):
    messages = _generate(tmp_path)
    flags = gf.scenario_coverage(messages)
    assert set(flags.keys()) == set(REQUIRED_FLAGS), f"flag set mismatch: {sorted(flags)}"
    missing = [name for name in REQUIRED_FLAGS if not flags[name]]
    assert not missing, f"missing scenario coverage: {missing}"


def test_output_filename_is_sample_chat_not_messages_json():
    assert gf.SAMPLE_PATH.name == "sample_chat.json"
    assert gf.SAMPLE_PATH.suffix == ".json"
    assert not gf.SAMPLE_PATH.name.endswith("_messages.json")
    assert gf.SAMPLE_PATH.exists(), "fixtures/sample_chat.json artifact must be generated"


def _assert_single_polarity(message):
    own = gf.POSITIVE_WORDS if message["sender"] == gf.SELF_SENDER else gf.NEGATIVE_WORDS
    opposite = gf.NEGATIVE_WORDS if message["sender"] == gf.SELF_SENDER else gf.POSITIVE_WORDS
    assert any(w in message["content"] for w in own), f"polarity missing: {message['content']}"
    assert not any(w in message["content"] for w in opposite), f"cross polarity: {message['content']}"


def test_two_speakers_one_conversation_seq_is_surrogate_only(tmp_path):
    messages = _generate(tmp_path)
    assert {m["sender"] for m in messages} == {gf.SELF_SENDER, gf.PEER_SENDER}
    assert {m["talker"] for m in messages} == {gf.TALKER}
    assert [m["seq"] for m in messages] == list(range(1, len(messages) + 1)), "seq must be unique 1..N"
    assert all(m["isSelf"] == (m["sender"] == gf.SELF_SENDER) for m in messages)


def _reply_gaps(timeline):
    """Split consecutive-pair gaps into A-replies (peer→self) and B-replies (self→peer)."""
    pairs = list(zip(timeline, timeline[1:]))
    a_gaps = [ms - prev_ms for (prev_sender, prev_ms), (sender, ms) in pairs
              if prev_sender == gf.PEER_SENDER and sender == gf.SELF_SENDER]
    b_gaps = [ms - prev_ms for (prev_sender, prev_ms), (sender, ms) in pairs
              if prev_sender == gf.SELF_SENDER and sender == gf.PEER_SENDER]
    return a_gaps, b_gaps


def test_asymmetric_responsiveness_a_fast_b_slow(tmp_path):
    messages = _generate(tmp_path)
    timeline = [(m["sender"], _to_ms(m["time"])) for m in messages]
    a_gaps, b_gaps = _reply_gaps(timeline)
    assert a_gaps and b_gaps, "need both reply directions in the corpus"
    assert max(a_gaps) <= 60_000, f"A must always reply within 60s, worst={max(a_gaps)}ms"
    slow_b = [gap for gap in b_gaps if gap > 600_000]
    assert len(slow_b) / len(b_gaps) >= 0.4, f"B must often be slow, got {len(slow_b)}/{len(b_gaps)}"


def test_all_positive_from_a_all_negative_from_b(tmp_path):
    messages = _generate(tmp_path)
    texts = [m for m in messages if m["type"] == "text"]
    assert len(texts) >= 100, f"expected a real text corpus, got {len(texts)}"
    for message in texts:
        _assert_single_polarity(message)
