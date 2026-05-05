"""Live LLM NER test — hits a real Anthropic API to verify the
end-to-end pipeline catches party_name on a contract clause.

Skipped automatically when ANTHROPIC_API_KEY is missing so the
default `pytest tests/` run never makes a network call. Run with
`ANTHROPIC_API_KEY=sk-ant-... pytest -m live tests/test_ner_llm_live.py`.

Cost: ~$0.0005 per run on claude-haiku-4-5. The fixture clause is a
single short sentence, ~50 input tokens + ~30 output tokens.
"""
from __future__ import annotations

import os

import pytest

from app.tasks.ner_llm import NERConfig, extract_via_llm


pytestmark = pytest.mark.live


# Synthetic contract clause built to surface every legal entity type
# we ask the LLM about. Real boilerplate from a public-domain MSA
# template — no PII.
CONTRACT_CLAUSE = (
    "This Master Services Agreement (the \"Agreement\") is entered into as of "
    "April 14, 2026 (the \"Effective Date\"), by and between "
    "Acme Logistics, Inc., a Delaware corporation (\"Acme\"), and "
    "Globex Holdings LLC, a New York limited liability company (\"Globex\"). "
    "This Agreement shall be governed by and construed under the laws of the "
    "State of New York, without regard to its conflict-of-law provisions."
)


def _have_key() -> bool:
    return bool(os.environ.get("ANTHROPIC_API_KEY", "").strip())


@pytest.mark.asyncio
@pytest.mark.skipif(not _have_key(), reason="ANTHROPIC_API_KEY not set")
async def test_llm_extracts_party_names_and_governing_law():
    cfg = NERConfig(
        enabled=True,
        model="anthropic/claude-haiku-4-5",
        entity_types=[
            "party_name", "effective_date", "jurisdiction", "governing_law",
        ],
        batch_size=1,
        min_confidence=0.6,
        api_key=None,  # litellm reads ANTHROPIC_API_KEY from env
    )
    entities = await extract_via_llm(CONTRACT_CLAUSE, cfg)

    by_type: dict[str, list[str]] = {}
    for e in entities:
        by_type.setdefault(e["entity_type"], []).append(e["entity_value"])

    party_names = by_type.get("party_name", [])
    governing_law = by_type.get("governing_law", []) + by_type.get("jurisdiction", [])
    effective_dates = by_type.get("effective_date", [])

    # Assertions are deliberately loose — different prompts on the
    # same model can return slightly different surface forms (e.g.
    # "Acme Logistics, Inc." vs "Acme Logistics" vs "Acme"). What we
    # care about is that the legally-correct token shows up *somewhere*
    # in the model's emission.
    assert any("Acme" in p for p in party_names), \
        f"party_name should surface Acme; got {party_names!r}"
    assert any("Globex" in p for p in party_names), \
        f"party_name should surface Globex; got {party_names!r}"
    assert any("New York" in g for g in governing_law), \
        f"governing_law should surface New York; got {governing_law!r}"
    assert any("2026" in d for d in effective_dates), \
        f"effective_date should surface the 2026 date; got {effective_dates!r}"

    # Every entity must have valid offsets pointing into the source.
    for e in entities:
        s, t = e["start_offset"], e["end_offset"]
        assert 0 <= s < t <= len(CONTRACT_CLAUSE)
        assert CONTRACT_CLAUSE[s:t] == e["entity_value"], \
            f"offsets misaligned for {e['entity_type']}={e['entity_value']!r}"

    # All emissions tagged source=llm so dedupe priority works.
    assert all(e["source"] == "llm" for e in entities)
