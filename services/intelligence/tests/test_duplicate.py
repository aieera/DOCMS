"""Tests for MinHash/SimHash utilities (no Redis needed)."""
from app.tasks.duplicate import _shingles, _minhash, _simhash, _hamming


def test_shingles_basic():
    text = "the quick brown fox jumps over the lazy dog"
    s = _shingles(text, k=3)
    assert len(s) > 0
    assert "the quick brown" in s


def test_minhash_similar_texts():
    # t2 differs from t1 in the LAST word only. With the default shingle
    # k=5 over a 9-word sentence, a change to the MIDDLE word lands in
    # every shingle (0 overlap — jaccard 0.0, same as unrelated text),
    # which is what the old "jumps→leaps" data degenerated to.
    t1 = "the quick brown fox jumps over the lazy dog"
    t2 = "the quick brown fox jumps over the lazy cat"
    t3 = "completely unrelated text about quantum physics and dark matter"

    m1 = _minhash(_shingles(t1))
    m2 = _minhash(_shingles(t2))
    m3 = _minhash(_shingles(t3))

    sim_12 = m1.jaccard(m2)
    sim_13 = m1.jaccard(m3)
    assert sim_12 > sim_13, "similar texts should have higher Jaccard"


def test_simhash_identical():
    t = "hello world this is a test"
    assert _simhash(t) == _simhash(t)


def test_hamming_zero_for_same():
    assert _hamming(42, 42) == 0


def test_hamming_counts_differences():
    assert _hamming(0b1111, 0b0000) == 4
