"""Chinese tokenizer for lover-instance: jieba + stopwords + custom dict.

Scope is deliberately narrow: text in, token list out. There is no
`word_counts` helper here on purpose — word frequency is aggregated in Go,
which is the only side that knows `is_self`.

Two rules matter to the Go side:

1. WeChat custom expression marks such as `[捂脸]` / `[旺柴]` are kept as ONE
   verbatim token. Go distinguishes word-cloud tokens from expression tokens
   purely by "is it wrapped in square brackets" (`WORD_RE`), so a mark that got
   stripped or split would silently vanish from the expression statistics.
2. `emoji_polarity` returns `None` for an unknown mark. It never falls back to
   `0.0`, because `0.0` means "known neutral" and the two states must stay
   distinguishable.
"""

from __future__ import annotations

import json
import pathlib
import re
from typing import Iterator

import jieba

# The single shared definition of "this token is a WeChat expression".
WORD_RE = re.compile(r"^\[.+\]$")

_DATA_DIR = pathlib.Path(__file__).resolve().parent.parent / "data"

# A mark as it appears in raw text: a bracketed span with no nested brackets.
_MARK_SPAN_RE = re.compile(r"\[[^\[\]]+\]")


def _read_word_list(path: pathlib.Path) -> frozenset[str]:
    """Read a UTF-8 word list; `#` starts a comment line, blanks are skipped."""
    words = set()
    for line in path.read_text(encoding="utf-8").splitlines():
        word = line.strip()
        if not word or word.startswith("#"):
            continue
        words.add(word)
    return frozenset(words)


def _read_user_dict(path: pathlib.Path) -> list[tuple[str, int | None, str | None]]:
    """Parse jieba userdict lines, skipping comments.

    Cannot hand the raw file to `jieba.load_userdict`: its own line regex would
    swallow `# ...` comment lines as literal dictionary words.
    """
    entries: list[tuple[str, int | None, str | None]] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        fields = line.strip().split()
        if not fields or fields[0].startswith("#"):
            continue
        word = fields[0]
        freq = int(fields[1]) if len(fields) > 1 and fields[1].isdigit() else None
        tag = fields[2] if len(fields) > 2 else None
        entries.append((word, freq, tag))
    return entries


def _read_emoji_map(path: pathlib.Path) -> dict[str, float]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    return {k: float(v) for k, v in raw.items() if not k.startswith("_")}


STOPWORDS: frozenset[str] = _read_word_list(_DATA_DIR / "stopwords.txt")
EMOJI_MAP: dict[str, float] = _read_emoji_map(_DATA_DIR / "emoji_map.json")

for _word, _freq, _tag in _read_user_dict(_DATA_DIR / "custom_dict.txt"):
    jieba.add_word(_word, freq=_freq, tag=_tag)
del _word, _freq, _tag


def _iter_segments(text: str) -> Iterator[tuple[bool, str]]:
    """Split text into (is_expression_mark, segment) pairs."""
    cursor = 0
    for match in _MARK_SPAN_RE.finditer(text):
        if match.start() > cursor:
            yield False, text[cursor:match.start()]
        yield True, match.group(0)
        cursor = match.end()
    if cursor < len(text):
        yield False, text[cursor:]


def tokenize(text: str) -> list[str]:
    """Segment `text` into tokens with stopwords removed.

    Expression marks are carried through verbatim, unsplit and unfiltered.
    """
    if not text or not text.strip():
        return []

    tokens: list[str] = []
    for is_mark, segment in _iter_segments(text):
        if is_mark:
            tokens.append(segment)
            continue
        for token in jieba.cut(segment):
            token = token.strip()
            if not token or token in STOPWORDS:
                continue
            tokens.append(token)
    return tokens


def emoji_polarity(mark: str) -> float | None:
    """Polarity prior for a WeChat expression mark, or None if unknown.

    Accepts either `[捂脸]` (token form) or `捂脸` (bare name). Unknown is
    `None`, never `0.0`: `0.0` is reserved for a mark that is known to be
    neutral, so callers can tell "no opinion" from "this is neutral".
    """
    key = mark.strip()
    if WORD_RE.match(key):
        key = key[1:-1]
    return EMOJI_MAP.get(key)