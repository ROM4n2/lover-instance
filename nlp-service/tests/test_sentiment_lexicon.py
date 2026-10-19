"""Task 6 RED: self-built dictionary sentiment baseline.

Honesty contract (ADR-0004) pinned by these tests:
- this is a *dictionary baseline polarity*, not a relationship diagnosis
- `intensity` is the HowNet-style degree factor normalized to 0..1; it is NOT
  DUTIR's 1-9 intensity, and no docstring may claim otherwise
- negation flips sign but stays weaker than the affirmative (x-0.5, not x-1)
"""

import pathlib

from app.sentiment_lexicon import (
    EMOTIONS,
    MODEL_VERSION,
    Score,
    score,
)

APP_DIR = pathlib.Path(__file__).resolve().parents[1] / "app"


def test_positive_sentence_has_positive_polarity():
    assert score("今天特别开心").polarity > 0


def test_negative_sentence_has_negative_polarity():
    assert score("我好难过").polarity < 0


def test_neutral_sentence_is_exactly_zero_and_neutral():
    result = score("明天下午三点在会议室开会")
    assert result.polarity == 0.0
    assert result.emotion == "neutral"


def test_negation_flips_sign_and_is_weaker_than_affirmative():
    negated = score("不开心").polarity
    affirmative = score("开心").polarity
    assert negated < 0 < affirmative
    # x-0.5, deliberately not x-1
    assert abs(negated) < abs(affirmative)


def test_degree_adverb_amplifies():
    assert score("非常开心").polarity > score("开心").polarity


def test_intensity_is_bounded_and_degree_sensitive():
    for text in ("开心", "非常开心", "不开心", "明天下午三点在会议室开会", ""):
        value = score(text).intensity
        assert 0.0 <= value <= 1.0
    assert score("非常开心").intensity > score("开心").intensity


def test_emotions_constant_is_exactly_seven_classes():
    assert EMOTIONS == ("好", "乐", "哀", "怒", "惧", "恶", "惊")
    assert len(EMOTIONS) == 7


def test_model_version_is_pinned():
    assert MODEL_VERSION == "lexicon-hownet-v1"


def test_score_returns_typed_model():
    result = score("好开心")
    assert isinstance(result, Score)
    assert isinstance(result.polarity, float)
    assert isinstance(result.intensity, float)
    assert isinstance(result.emotion, str)
    assert -1.0 <= result.polarity <= 1.0
    assert result.emotion in EMOTIONS or result.emotion == "neutral"


def test_emotion_label_comes_from_the_matched_words():
    assert score("我好难过").emotion == "哀"
    assert score("太生气了").emotion == "怒"


def test_emoji_mark_contributes_polarity():
    assert score("[大哭]").polarity < 0
    assert score("[大笑]").polarity > 0


def test_every_emotion_class_has_at_least_20_words():
    from app.sentiment_lexicon import LEXICON

    counts = {emotion: 0 for emotion in EMOTIONS}
    for emotion in LEXICON.values():
        counts[emotion] += 1
    for emotion in EMOTIONS:
        assert counts[emotion] >= 20, f"{emotion} only has {counts[emotion]} words"


def test_no_relationship_diagnosis_wording_anywhere():
    """ADR-0004 honest naming: the module must not claim to diagnose."""
    source = (APP_DIR / "sentiment_lexicon.py").read_text(encoding="utf-8")
    for forbidden in ("关系诊断", "Gottman", "gottman", "diagnosis", "危险信号", "军师"):
        assert forbidden not in source