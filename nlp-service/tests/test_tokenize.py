"""Task 5 RED: jieba tokenizer + stopwords + custom dict + WeChat expression map.

Contract pinned here:
- stopwords are removed, custom-dict words stay whole
- a WeChat expression mark such as `[捂脸]` survives as ONE verbatim token
  (Go distinguishes word-cloud tokens from expression tokens purely by
  "is it wrapped in square brackets")
- `emoji_polarity` returns None for an unknown mark, never 0.0
- `WORD_RE` is the single shared definition of "expression token"
"""

import unicodedata

from app.tokenize import WORD_RE, countable_tokens, emoji_polarity, tokenize


def test_stopwords_are_removed():
    tokens = tokenize("我的是了")
    assert "的" not in tokens
    assert "是" not in tokens
    assert "了" not in tokens
    assert "我" in tokens


def test_custom_dict_keeps_slang_whole():
    # Default jieba cuts this into 蚌埠 / 住 / 了; the custom dict must not.
    assert "蚌埠住了" in tokenize("我蚌埠住了")
    # Default jieba cuts this into 栓 / Q.
    assert "栓Q" in tokenize("栓Q了")


def test_wechat_expression_survives_verbatim_as_one_token():
    tokens = tokenize("今天好累[捂脸]")
    assert "[捂脸]" in tokens
    # must not be stripped or split into bracket + inner word
    assert "捂脸" not in tokens
    assert "[" not in tokens
    assert "]" not in tokens


def test_word_re_defines_expression_tokens():
    assert WORD_RE.match("[捂脸]")
    assert WORD_RE.match("[旺柴]")
    assert not WORD_RE.match("捂脸")
    assert not WORD_RE.match("[")
    assert not WORD_RE.match("]")
    # anchored: a bracket span inside a sentence is not itself an expression token
    assert not WORD_RE.match("a[捂脸]b")


def test_emoji_polarity_known_mark():
    value = emoji_polarity("[捂脸]")
    assert value is not None
    assert -1.0 <= value <= 1.0
    # accepts the bare name too, for callers holding an already-unwrapped mark
    assert emoji_polarity("捂脸") == value


def test_emoji_polarity_unknown_is_none_not_zero():
    assert emoji_polarity("[不存在的表情]") is None


def test_emoji_polarity_known_neutral_stays_distinguishable_from_unknown():
    # 0.0 means "known neutral"; None means "unknown". They must not collapse.
    assert emoji_polarity("[吃瓜]") == 0.0
    assert emoji_polarity("[吃瓜]") is not None


def test_tokenize_empty_input():
    assert tokenize("") == []
    assert tokenize("   ") == []

def test_countable_tokens_drops_punctuation():
    """jieba emits `，` and `！` as tokens of their own, and nothing in the
    stopword list removes them: that list is about meaningless words, and
    punctuation is not a word. On the first real conversation this put `，`
    third in one side's word cloud and `！` third in the other's."""
    tokens = countable_tokens("今天特别开心，我们一起去看电影！")

    assert "，" not in tokens
    assert "！" not in tokens
    for token in tokens:
        assert not all(unicodedata.category(c).startswith("P") for c in token), token


def test_countable_tokens_keeps_words_symbols_and_expression_marks():
    """Only punctuation is removed. Expression marks are counted deliberately,
    and a raw emoji is a symbol the expression map may still have an opinion
    about — neither is punctuation."""
    tokens = countable_tokens("好开心[捂脸]😀")

    # The custom dictionary keeps 好开心 together as one token, so this
    # asserts on the content rather than on a particular cut.
    assert any("开心" in token for token in tokens), tokens
    assert "[捂脸]" in tokens
    assert "😀" in tokens


def test_countable_tokens_does_not_change_the_scoring_stream():
    """The scoring path measures its windows in token positions, so removing a
    token from that stream would move every hit closer to its modifier. The two
    views must agree everywhere except on punctuation."""
    text = "我不太开心，但是还可以"
    scoring = [t for t in tokenize(text) if not all(unicodedata.category(c).startswith("P") for c in t)]

    assert countable_tokens(text) == scoring
