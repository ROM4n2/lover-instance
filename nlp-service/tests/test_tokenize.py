"""Task 5 RED: jieba tokenizer + stopwords + custom dict + WeChat expression map.

Contract pinned here:
- stopwords are removed, custom-dict words stay whole
- a WeChat expression mark such as `[捂脸]` survives as ONE verbatim token
  (Go distinguishes word-cloud tokens from expression tokens purely by
  "is it wrapped in square brackets")
- `emoji_polarity` returns None for an unknown mark, never 0.0
- `WORD_RE` is the single shared definition of "expression token"
"""

from app.tokenize import WORD_RE, emoji_polarity, tokenize


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