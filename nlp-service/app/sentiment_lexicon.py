"""Dictionary baseline polarity for Chinese chat messages.

WHAT THIS IS
------------
A hand-built dictionary baseline. It looks up emotion words, applies HowNet-style
degree-adverb amplification and a bounded negation window, and reports a coarse
polarity plus a dominant emotion *category*. It is not a model, not a classifier
and not a psychological assessment of anyone; it cannot read sarcasm, pet names
or in-jokes (see ADR-0004).

`intensity` IS NOT DUTIR'S 1-9 INTENSITY
----------------------------------------
`intensity` is the HowNet-style **degree factor normalized to 0..1**
(`(mean_factor - 0.5) / 1.5`). It is derived from `data/hownet_degree.txt`,
which is a small self-written table, NOT the HowNet distribution. The original
plan assumed `cnsenti` shipped DUTIR's 1-9 intensity; measurement showed it does
not, so `cnsenti` was dropped and this dimension was redefined. Do not describe
this field as DUTIR intensity anywhere.

METHOD
------
- Every emotion word carries a class (one of `EMOTIONS`); the class fixes its
  sign and base magnitude (`CLASS_POLARITY`).
- A degree adverb within `DEGREE_WINDOW` preceding tokens multiplies the
  magnitude (0.5..2.0, clamped).
- A negation within `NEGATION_WINDOW` preceding tokens multiplies by
  `NEGATION_COEFFICIENT` (-0.5). Negated utterances are therefore weaker than
  their affirmative counterparts, which is the intended, deliberately
  conservative behaviour.
- `polarity` squashes the raw sum into (-1, 1) via `raw / (1 + |raw|)`.
- `emotion` is the class with the largest absolute contribution, or "neutral".
  Note: negation flips polarity, not the category — an "不开心" still reports the
  乐 word family, because that is the word family that was actually matched.
"""

from __future__ import annotations

import pathlib

from pydantic import BaseModel

from app.tokenize import WORD_RE, emoji_polarity, tokenize

MODEL_VERSION = "lexicon-hownet-v1"

EMOTIONS: tuple[str, ...] = ("好", "乐", "哀", "怒", "惧", "恶", "惊")

NEUTRAL_EMOTION = "neutral"

# Sign and base magnitude per emotion class. Hand-assigned priors.
CLASS_POLARITY: dict[str, float] = {
    "好": 1.0,
    "乐": 0.9,
    "哀": -0.9,
    "怒": -0.9,
    "惧": -0.8,
    "恶": -0.8,
    "惊": -0.2,
}

NEGATION_WINDOW = 5
DEGREE_WINDOW = 3
NEGATION_COEFFICIENT = -0.5
DEGREE_MIN = 0.5
DEGREE_MAX = 2.0
DEFAULT_DEGREE = 1.0

# Longest lexicon word is 4 characters, so a match spans at most 4 tokens.
MAX_RUN_TOKENS = 4

_DATA_DIR = pathlib.Path(__file__).resolve().parent.parent / "data"


def _read_pairs(path: pathlib.Path) -> dict[str, str]:
    pairs: dict[str, str] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        fields = line.strip().split()
        if not fields or fields[0].startswith("#"):
            continue
        if len(fields) != 2:
            raise ValueError(f"{path.name}: malformed line {line!r}")
        pairs[fields[0]] = fields[1]
    return pairs


def _read_factors(path: pathlib.Path) -> dict[str, float]:
    return {word: float(value) for word, value in _read_pairs(path).items()}


def _read_words(path: pathlib.Path) -> frozenset[str]:
    """Read a one-word-per-line list; `#` starts a comment line."""
    words = set()
    for line in path.read_text(encoding="utf-8").splitlines():
        word = line.strip()
        if not word or word.startswith("#"):
            continue
        words.add(word)
    return frozenset(words)


def _load_lexicon() -> dict[str, str]:
    lexicon: dict[str, str] = {}
    for line in (_DATA_DIR / "emotion_lexicon.txt").read_text(encoding="utf-8").splitlines():
        fields = line.strip().split()
        if not fields or fields[0].startswith("#"):
            continue
        if len(fields) != 2:
            raise ValueError(f"emotion_lexicon.txt: malformed line {line!r}")
        word, emotion = fields
        if emotion not in EMOTIONS:
            raise ValueError(f"emotion_lexicon.txt: unknown class {emotion!r}")
        if word in lexicon:
            raise ValueError(f"emotion_lexicon.txt: duplicate word {word!r}")
        lexicon[word] = emotion
    return lexicon


LEXICON: dict[str, str] = _load_lexicon()
DEGREE: dict[str, float] = _read_factors(_DATA_DIR / "hownet_degree.txt")
NEGATION: frozenset[str] = _read_words(_DATA_DIR / "hownet_negation.txt")


class Score(BaseModel):
    """Baseline polarity for one message.

    `polarity`  in [-1, 1], negative = unpleasant
    `emotion`   one of `EMOTIONS`, or "neutral"
    `intensity` degree factor normalized to [0, 1] (NOT DUTIR's 1-9)
    """

    polarity: float
    emotion: str
    intensity: float


def _match_at(tokens: list[str], index: int) -> tuple[str, int, str] | None:
    """Longest lexicon word formed by joining tokens starting at `index`."""
    limit = min(MAX_RUN_TOKENS, len(tokens) - index)
    for span in range(limit, 0, -1):
        candidate = "".join(tokens[index:index + span])
        emotion = LEXICON.get(candidate)
        if emotion is not None:
            return candidate, span, emotion
    return None


def _char_match_at(text: str, position: int) -> tuple[str, str] | None:
    for end in range(len(text), position, -1):
        candidate = text[position:end]
        emotion = LEXICON.get(candidate)
        if emotion is not None:
            return candidate, emotion
    return None


def _has_match_from(text: str, position: int) -> bool:
    return _char_match_at(text, position) is not None


def _scan_fused_token(token: str) -> list[tuple[str, str, float, bool]]:
    """Score lexicon words fused inside a single jieba token.

    Needed because jieba emits everyday phrases such as ``太好了`` / ``我爱你``
    as one token, hiding the emotion word. Returns (word, emotion, factor, negated).
    """
    found: list[tuple[str, str, float, bool]] = []
    pending_degree: float | None = None
    negated = False
    position = 0

    while position < len(token):
        match = _char_match_at(token, position)
        if match is not None:
            word, emotion = match
            # An ambiguous word that is also a degree adverb ("好" in "好难过")
            # modifies the match that follows it instead of scoring itself.
            if word in DEGREE and _has_match_from(token, position + len(word)):
                pending_degree = DEGREE[word]
                position += len(word)
                continue
            factor = pending_degree if pending_degree is not None else DEFAULT_DEGREE
            found.append((word, emotion, factor, negated))
            pass
            negated = False
            position += len(word)
            continue

        char = token[position]
        if char in NEGATION:
            negated = True
        elif char in DEGREE and _has_match_from(token, position + 1):
            pending_degree = DEGREE[char]
        position += 1

    return found


def _collect_items(tokens: list[str]) -> list[tuple[str, str, float, bool]]:
    """Walk the token stream and return the hits it contains.

    Each hit is `(kind, key, magnitude, negated)`:
      kind="word"  -> key is the emotion class, magnitude is the degree factor
      kind="emoji" -> key is the expression token, magnitude is its polar prior
    """
    items: list[tuple[str, str, float, bool]] = []
    consumed: set[int] = set()
    negated_until = -1
    degree_until = -1
    degree_value = DEFAULT_DEGREE

    for index, token in enumerate(tokens):
        if index in consumed:
            continue

        if WORD_RE.match(token):
            value = emoji_polarity(token)
            if value is not None and value != 0.0:
                items.append(("emoji", token, value, False))
            continue

        run = _match_at(tokens, index)
        if run is not None:
            word, span, emotion = run
            if token in DEGREE and word == token and any(
                _match_at(tokens, j) is not None
                for j in range(index + 1, min(index + DEGREE_WINDOW + 1, len(tokens)))
            ):
                # Ambiguous degree/emotion word acting as a modifier here.
                degree_value = DEGREE[token]
                degree_until = index + DEGREE_WINDOW
                continue
            factor = degree_value if index < degree_until else DEFAULT_DEGREE
            items.append(("word", emotion, factor, index < negated_until))
            consumed.update(range(index, index + span))
            continue

        if token in NEGATION:
            negated_until = index + NEGATION_WINDOW
            continue

        if token in DEGREE:
            degree_value = DEGREE[token]
            degree_until = index + DEGREE_WINDOW
            continue

        # No lexicon word at token granularity: the word may be fused inside a
        # longer jieba token ("太好了", "我爱你").
        for word, emotion, inner_factor, inner_negated in _scan_fused_token(token):
            outer_factor = degree_value if index < degree_until else DEFAULT_DEGREE
            factor = min(max(outer_factor * inner_factor, DEGREE_MIN), DEGREE_MAX)
            items.append(("word", emotion, factor, inner_negated or index < negated_until))

    return items


def _normalize_intensity(factors: list[float]) -> float:
    if not factors:
        return 0.0
    mean = sum(factors) / len(factors)
    normalized = (mean - DEGREE_MIN) / (DEGREE_MAX - DEGREE_MIN)
    return min(max(normalized, 0.0), 1.0)


def score(text: str) -> Score:
    """Baseline polarity / dominant emotion category / degree intensity."""
    tokens = tokenize(text)
    items = _collect_items(tokens)

    if not items:
        return Score(polarity=0.0, emotion=NEUTRAL_EMOTION, intensity=0.0)

    raw = 0.0
    per_emotion: dict[str, float] = {emotion: 0.0 for emotion in EMOTIONS}
    factors: list[float] = []

    for kind, key, magnitude, negated in items:
        if kind == "word":
            contribution = CLASS_POLARITY[key] * magnitude
            per_emotion[key] += contribution
            factors.append(magnitude)
        else:
            # WeChat expression mark: raw polarity prior, no degree dimension.
            contribution = magnitude
            per_emotion["乐" if contribution > 0 else "哀"] += contribution
        if negated:
            contribution *= NEGATION_COEFFICIENT
        raw += contribution

    dominant = max(EMOTIONS, key=lambda emotion: abs(per_emotion[emotion]))
    emotion = NEUTRAL_EMOTION if abs(per_emotion[dominant]) == 0.0 else dominant

    return Score(
        polarity=raw / (1.0 + abs(raw)),
        emotion=emotion,
        intensity=_normalize_intensity(factors),
    )