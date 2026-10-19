"""合成语料 fixture 生成器 — chatlog-keeper 导出格式的双人聊天语料。

字段名与消息类型码取自 M0 实测：对微信 4.1.9.30 的一份 chatlog-keeper 0.3.7
真实导出（32624 条）逐字段核对所得，不是设计阶段的假设。产物可直接喂给
backend-go/internal/source/chatlogkeeper。

纯标准库（json/random only）。固定 seed 产出字节级稳定的文件。
"""
import json
import random
from collections import Counter
from datetime import datetime, timedelta, timezone
from pathlib import Path

# chatlog-keeper 实测字段。server_id 是微信原生消息 ID（M0 实测 32624 条中
# 3 条为 null），source_offset 是导出游标（实测 100% 非空且唯一），二者共同
# 构成 canonical 唯一键：server_id 优先，source_offset 兜底。
REQUIRED_FIELDS = (
    "ts", "sender", "sender_wxid", "chat_room", "conversation_id", "content",
    "msg_type", "is_self", "account_id", "thread_id", "server_id",
    "source_offset", "conversation_type", "is_group_chat",
)

REQUIRED_FLAGS = ("撤回消息", "系统消息", "红包", "图片语音占位", "自定义表情",
                  "时间戳混用", "深夜消息", "同秒连发", "唯一键兜底")

# 微信消息类型码实测值：1 文本 / 3 图片 / 34 语音 / 49 应用消息（含红包）/
# 10000 系统消息。撤回在真实数据里就是以 10000 系统消息形式出现、正文含
# "撤回"二字，因此 revoke 与 system 共用 10000，靠正文区分。
MSG_TYPES = {
    "text": 1,
    "image": 3,
    "voice": 34,
    "redpacket": 49,
    "system": 10000,
    "revoke": 10000,
}

SELF_WXID = "wxid_self_a"     # speaker A: fast, always positive
PEER_WXID = "wxid_peer_b"     # speaker B: often slow, always negative
SELF_LABEL = "我"
PEER_LABEL = "对方"
ACCOUNT_ID = "wxid_self_a_abcd"
THREAD_ID = "thread_" + "0" * 39
CONVERSATION_ID = "wxid_peer_b"
SERVER_ID_NULL_EVERY = 331   # deterministic sprinkle of null server_id rows

POSITIVE_WORDS = ("开心", "喜欢", "爱你", "谢谢", "期待", "幸福", "太棒了", "想你")
NEGATIVE_WORDS = ("难过", "好累", "好烦", "生气", "委屈", "无聊", "好困", "焦虑")
POSITIVE_PHRASES = ("我们周末去看电影吧", "今天天气真好", "等你回家一起吃饭",
                    "我把票都买好了", "记得多喝水哦", "晚上给你留了汤")
NEGATIVE_PHRASES = ("又加班到现在", "地铁上挤死了", "明天还要早起",
                    "晚饭又随便对付了", "回家的路好远", "工位空调又坏了")
EMOJI_MARKS = ("[捂脸]", "[旺柴]")
PLACEHOLDERS = {"image": "[图片]", "voice": "[语音]", "redpacket": "[微信红包] 手气最佳请接好"}
SYSTEM_CONTENT = "系统消息：你已添加了对方，现在可以开始聊天"
REVOKE_CONTENT = "你撤回了一条消息"

SESSION_HOURS = (9, 11, 13, 15, 17, 19, 21, 22, 23, 23, 23)  # 23 deliberately repeated: 深夜 guarantee
SLOW_REPLY_PROBABILITY = 0.6   # B replies >10min this often
INSERT_PROBABILITY = 0.25      # chance of a system/revoke insert per pair
TEXT_EMOJI_PROBABILITY = 0.15
BURST_SIZE = 50                # messages sharing one identical ms timestamp

DAY_MS = 86_400_000
HOUR_MS = 3_600_000
SECONDS_LIMIT = 100_000_000_000        # values below this are epoch seconds
MILLISECONDS_LIMIT = 100_000_000_000_000  # below this: ms; at/above: µs
CST_OFFSET_MS = 8 * HOUR_MS
TIME_ZONE = timezone(timedelta(hours=8))
BASE_EPOCH_MS = int(datetime(2024, 1, 1, tzinfo=TIME_ZONE).timestamp() * 1000)

SAMPLE_PATH = Path(__file__).with_name("sample_chat.json")


def generate_chat(out_path, num_days=90, seed=42):
    """Generate the canonical two-person corpus and write it to out_path as bytes."""
    if num_days < 1:
        raise ValueError("num_days must be >= 1, got %r" % (num_days,))
    rng = random.Random(seed)
    messages = []
    clock_ms = 0
    for day in range(num_days):
        clock_ms = _emit_session(_session_start_ms(day, clock_ms, rng), messages, rng)
    _emit_burst(clock_ms, messages, rng)
    payload = json.dumps(messages, ensure_ascii=False, indent=2)
    target = Path(out_path)
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(payload.encode("utf-8"))
    return messages


def scenario_coverage(messages):
    """Return the 9 required scenario marker flags for a corpus."""
    if not messages:
        raise ValueError("scenario_coverage requires at least one message")
    types = {message["msg_type"] for message in messages}
    joined = "".join(message["content"] for message in messages)
    units = {_time_unit(message["ts"]) for message in messages}
    hours = [((_to_ms(message["ts"]) + CST_OFFSET_MS) // HOUR_MS) % 24 for message in messages]
    return {
        "撤回消息": "撤回" in joined,
        "系统消息": "系统消息" in joined,
        "红包": MSG_TYPES["redpacket"] in types,
        "图片语音占位": MSG_TYPES["image"] in types and MSG_TYPES["voice"] in types,
        "自定义表情": all(mark in joined for mark in EMOJI_MARKS),
        "时间戳混用": {"s", "ms", "us"} <= units,
        "深夜消息": any(hour >= 23 for hour in hours),
        "同秒连发": _largest_identical_group(messages) >= 2,
        "唯一键兜底": any(message["server_id"] is None for message in messages),
    }


def _session_start_ms(day, now_ms, rng):
    """Deterministic per-day session start (CST hours), never moving backwards."""
    hour = rng.choice(SESSION_HOURS)
    minute = rng.randint(0, 59)
    scheduled = BASE_EPOCH_MS + day * DAY_MS + hour * HOUR_MS + minute * 60_000
    return max(scheduled, now_ms + 60_000)


def _emit_session(start_ms, messages, rng):
    when = start_ms
    for pair in range(rng.randint(3, 6)):
        when = _emit_peer_turn(when, messages, rng, is_session_open=pair == 0)
        when = _emit_self_reply(when, messages, rng)
        when = _maybe_emit_insert(when, messages, rng)
    return when


def _emit_peer_turn(now_ms, messages, rng, is_session_open):
    """B opens/speaks: session opener lands exactly on the scheduled hour."""
    gap = 0 if is_session_open else _peer_gap_ms(rng)
    when = now_ms + gap
    kind = _pick_peer_kind(rng)
    content = _peer_content(kind, rng)
    _append_message(messages, when, PEER_WXID, PEER_LABEL, kind, content, rng)
    return when


def _emit_self_reply(now_ms, messages, rng):
    """A always replies within 5-50 seconds."""
    when = now_ms + rng.randint(5_000, 50_000)
    kind = _pick_self_kind(rng)
    content = _self_content(rng) if kind == "text" else PLACEHOLDERS[kind]
    _append_message(messages, when, SELF_WXID, SELF_LABEL, kind, content, rng)
    return when


def _maybe_emit_insert(now_ms, messages, rng):
    """Occasional 系统消息 / 撤回消息 insert after A's reply."""
    if rng.random() >= INSERT_PROBABILITY:
        return now_ms
    kind = "system" if rng.random() < 0.5 else "revoke"
    wxid = PEER_WXID if kind == "system" else SELF_WXID
    label = PEER_LABEL if kind == "system" else SELF_LABEL
    content = SYSTEM_CONTENT if kind == "system" else REVOKE_CONTENT
    when = now_ms + rng.randint(3_000, 15_000)
    _append_message(messages, when, wxid, label, kind, content, rng)
    return when


def _emit_burst(now_ms, messages, rng):
    """50 same-sender messages sharing one identical millisecond timestamp."""
    burst_ms = now_ms + rng.randint(10_000, 50_000)
    for index in range(BURST_SIZE):
        content = f"{POSITIVE_WORDS[index % len(POSITIVE_WORDS)]}！{EMOJI_MARKS[index % len(EMOJI_MARKS)]}"
        _append_message(messages, burst_ms, SELF_WXID, SELF_LABEL, "text", content, rng, unit="ms")
    return burst_ms


def _peer_gap_ms(rng):
    """B: often slow (>10min), sometimes within 1-9 minutes."""
    if rng.random() < SLOW_REPLY_PROBABILITY:
        return rng.randint(11 * 60_000, 45 * 60_000)
    return rng.randint(60_000, 9 * 60_000)


def _pick_peer_kind(rng):
    roll = rng.random()
    if roll < 0.55:
        return "text"
    if roll < 0.70:
        return "image"
    if roll < 0.85:
        return "voice"
    return "redpacket"


def _pick_self_kind(rng):
    roll = rng.random()
    if roll < 0.70:
        return "text"
    if roll < 0.85:
        return "image"
    return "voice"


def _peer_content(kind, rng):
    if kind != "text":
        return PLACEHOLDERS[kind]
    return f"{rng.choice(NEGATIVE_WORDS)}，{rng.choice(NEGATIVE_PHRASES)}"


def _self_content(rng):
    text = f"{rng.choice(POSITIVE_WORDS)}，{rng.choice(POSITIVE_PHRASES)}"
    if rng.random() >= TEXT_EMOJI_PROBABILITY:
        return text
    return text + rng.choice(EMOJI_MARKS)


def _append_message(messages, when_ms, wxid, label, kind, content, rng, unit=None):
    index = len(messages)
    payload = {
        "ts": _stamp(when_ms, rng, unit),
        "sender": label,
        "sender_wxid": wxid,
        "chat_room": CONVERSATION_ID,
        "conversation_id": CONVERSATION_ID,
        "content": content,
        "msg_type": MSG_TYPES[kind],
        "is_self": wxid == SELF_WXID,
        "account_id": ACCOUNT_ID,
        "thread_id": THREAD_ID,
        # A deterministic sprinkle of null server_id rows keeps the corpus
        # exercising the source_offset fallback that M0 measured on real data.
        "server_id": None if index % SERVER_ID_NULL_EVERY == 0 else f"7{index:018d}",
        "source_offset": f"wechat_export:{index}",
        "conversation_type": "direct",
        "is_group_chat": False,
    }
    messages.append(payload)


def _stamp(ms, rng, unit=None):
    """Render an epoch-ms instant as mixed s/ms/µs wire value."""
    if unit == "ms":
        return int(ms)
    if unit == "s":
        return int(ms // 1000)
    if unit == "us":
        return int(ms * 1000)
    roll = rng.random()
    if roll < 0.15:
        return int(ms // 1000)
    if roll < 0.90:
        return int(ms)
    return int(ms * 1000)


def _time_unit(value):
    if value < SECONDS_LIMIT:
        return "s"
    if value < MILLISECONDS_LIMIT:
        return "ms"
    return "us"


def _to_ms(value):
    """Normalize a mixed s/ms/µs timestamp to epoch milliseconds."""
    if value < SECONDS_LIMIT:
        return value * 1000
    if value < MILLISECONDS_LIMIT:
        return value
    return value // 1000


def _largest_identical_group(messages):
    counts = Counter(_to_ms(message["ts"]) for message in messages)
    return max(counts.values())


if __name__ == "__main__":
    corpus = generate_chat(out_path=SAMPLE_PATH)
    print(f"wrote {SAMPLE_PATH.name}: {len(corpus)} messages, {SAMPLE_PATH.stat().st_size} bytes")