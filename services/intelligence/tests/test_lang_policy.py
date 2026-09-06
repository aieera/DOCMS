"""QA SD-18 (retest): an English PDF detected as Estonian at 50% still
DROVE translation/NER routing even after the display started saying
"uncertain". Below the confidence floor a detection is a coin flip —
routing must fall back to the tenant default instead of acting on it.
The stored detection (with its confidence) is untouched; only routing
decisions pass through this policy."""
from app.lang_policy import ROUTING_MIN_CONFIDENCE, routable_language


def test_confident_detection_routes():
    assert routable_language("ar", 0.98) == "ar"


def test_coin_flip_does_not_route():
    assert routable_language("et", 0.50) is None


def test_threshold_matches_display_floor():
    # The viewer hides the badge below 0.75; routing uses the same line.
    assert ROUTING_MIN_CONFIDENCE == 0.75
    assert routable_language("et", 0.7499) is None
    assert routable_language("et", 0.75) == "et"


def test_missing_values_do_not_route():
    assert routable_language(None, 0.9) is None
    assert routable_language("", 0.9) is None
    assert routable_language("en", None) is None
