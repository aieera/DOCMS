"""LLM-backed NER for the novel entity types regex+SpaCy can't reach
(governing_law, effective_date, party_name, account_number, tax_id,
patient_id, address, national_id).

Per ADR 0061 this is opt-in per tenant via the ner_config table.
Default model is claude-haiku-4-5 (Anthropic; supports BAA which is
required for the medical use case). litellm gives us the same call
shape across providers, so a tenant can switch to ollama/llama3.1:8b
for on-prem without touching this code.

The LLM is asked ONLY for the configured types and forced into JSON
via litellm's response_format. We then re-validate every offset by
substring search — LLMs hallucinate offsets — and drop hits that
don't reproduce.
"""
from __future__ import annotations

import json
import logging
from dataclasses import dataclass
from typing import Any

log = logging.getLogger(__name__)

# Default config used when ner_config has no row for this tenant.
DEFAULT_NER_CONFIG: dict[str, Any] = {
    "llm_enabled": False,
    "llm_model": "claude-haiku-4-5",
    "llm_entity_types": [
        "party_name", "effective_date", "jurisdiction", "governing_law",
        "account_number", "tax_id", "patient_id", "address", "national_id",
    ],
    "llm_batch_size": 5,
    "llm_min_confidence": 0.6,
}


@dataclass
class NERConfig:
    enabled: bool
    model: str
    entity_types: list[str]
    batch_size: int
    min_confidence: float
    # api_key_plaintext is hydrated only inside the worker (via
    # secrets.decrypt_tenant_secret). None means "no key configured" or
    # "decrypt failed" — both treated the same: skip the LLM call.
    api_key: str | None = None

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "NERConfig":
        merged = {**DEFAULT_NER_CONFIG, **(d or {})}
        return cls(
            enabled=bool(merged["llm_enabled"]),
            model=str(merged["llm_model"]),
            entity_types=list(merged["llm_entity_types"]),
            batch_size=int(merged["llm_batch_size"]),
            min_confidence=float(merged["llm_min_confidence"]),
            api_key=merged.get("api_key"),
        )


def _build_prompt(text: str, target_types: list[str]) -> str:
    """The prompt is intentionally narrow: only ask for the types regex
    + SpaCy can't handle. Cap text at 8K chars — beyond that the cost
    isn't worth the precision."""
    snippet = text[:8000]
    types_csv = ", ".join(target_types)
    return (
        f"Extract the following entity types from the text below: {types_csv}.\n"
        "Return JSON with this exact shape: "
        '{"entities": [{"type": "...", "value": "...", "char_start": 0, "char_end": 0, "confidence": 0.0}]}\n'
        "char_start and char_end are character offsets into the text. "
        "Only include entities you are confident about (confidence >= 0.6). "
        "If the text contains none of these types, return {\"entities\": []}.\n\n"
        f"---TEXT---\n{snippet}\n---END TEXT---"
    )


def _validate_and_realign(
    raw_entities: list[dict[str, Any]],
    text: str,
    cfg: NERConfig,
) -> list[dict[str, Any]]:
    """LLMs hallucinate char offsets. For each emitted entity, verify
    that text[start:end] actually equals value; if not, fall back to
    a single substring search and use that. Drop entities we can't
    locate at all, or whose confidence is below the cutoff."""
    out: list[dict[str, Any]] = []
    for raw in raw_entities:
        try:
            etype = str(raw.get("type") or "").strip()
            value = str(raw.get("value") or "").strip()
            conf = float(raw.get("confidence") or 0.0)
        except (TypeError, ValueError):
            continue
        if not etype or not value or etype not in cfg.entity_types:
            continue
        if conf < cfg.min_confidence:
            continue

        start = int(raw.get("char_start") or -1)
        end = int(raw.get("char_end") or -1)
        # Validate the offsets the LLM emitted.
        if 0 <= start < end <= len(text) and text[start:end] == value:
            pass  # LLM got it right
        else:
            # Fall back to a single substring search (no fuzzy matching;
            # if the value isn't literally in the text, we don't trust it).
            idx = text.find(value)
            if idx < 0:
                log.debug("ner_llm: dropped %s=%r — not found in source", etype, value)
                continue
            start, end = idx, idx + len(value)

        out.append({
            "entity_type": etype,
            "entity_value": value,
            "start_offset": start,
            "end_offset": end,
            "confidence": conf,
            "is_pii": etype in {
                "name", "email", "phone", "address", "national_id",
                "patient_id", "dob", "account_number",
            },
            "source": "llm",
        })
    return out


async def extract_via_llm(text: str, cfg: NERConfig) -> list[dict[str, Any]]:
    """Returns NER entities from the LLM, validated and aligned to text
    offsets. Returns [] on any failure path — NER is best-effort, never
    block the pipeline on a hallucinating model.

    Key resolution: cfg.api_key (per-tenant, set via the admin UI) takes
    precedence; if None we fall through to litellm's own env-var lookup
    (ANTHROPIC_API_KEY / OPENAI_API_KEY) so an operator who prefers env
    config still works. If neither is set, the call fails on the
    provider side and we swallow it like any other LLM failure.
    """
    if not cfg.enabled or not text.strip():
        return []
    try:
        # Lazy import: container builds without litellm in tests.
        import litellm  # type: ignore
    except ImportError:
        log.warning("ner_llm: litellm not installed; skipping LLM NER")
        return []

    prompt = _build_prompt(text, cfg.entity_types)
    kwargs: dict[str, Any] = {
        "model": cfg.model,
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0,
        "max_tokens": 2000,
        "timeout": 30,
    }
    # litellm forwards response_format={type:json_object} to Anthropic
    # as a tool-use schema that fails draft-2020-12 validation in
    # current Claude APIs (verified live with claude-haiku-4-5). We
    # only set the param for OpenAI-family providers where the JSON
    # mode is natively supported; the prompt's "Return JSON with this
    # exact shape" instruction is enough for Claude in practice, and
    # _validate_and_realign drops anything malformed.
    if cfg.model.startswith(("openai/", "gpt-")):
        kwargs["response_format"] = {"type": "json_object"}
    if cfg.api_key:
        kwargs["api_key"] = cfg.api_key
    try:
        resp = await litellm.acompletion(**kwargs)  # type: ignore
    except Exception as e:  # noqa: BLE001 — we *do* want to swallow everything here
        log.warning("ner_llm: %s call failed: %s", cfg.model, e)
        return []

    try:
        content = resp["choices"][0]["message"]["content"]
        # Without response_format=json_object (Anthropic path), the
        # model sometimes wraps its JSON in ```json ... ``` fences or
        # prefaces it with a sentence. Strip both — find the first {
        # and the last matching } and parse that slice.
        cleaned = content.strip()
        if cleaned.startswith("```"):
            cleaned = cleaned.split("```", 2)[1]
            if cleaned.startswith("json"):
                cleaned = cleaned[4:]
            cleaned = cleaned.strip().rstrip("`").strip()
        first_brace = cleaned.find("{")
        last_brace = cleaned.rfind("}")
        if first_brace >= 0 and last_brace > first_brace:
            cleaned = cleaned[first_brace : last_brace + 1]
        parsed = json.loads(cleaned)
        raw_entities = parsed.get("entities", [])
        if not isinstance(raw_entities, list):
            return []
    except (KeyError, IndexError, json.JSONDecodeError, TypeError) as e:
        log.warning("ner_llm: malformed response: %s", e)
        return []

    return _validate_and_realign(raw_entities, text, cfg)


async def load_ner_config(tenant_id: str) -> NERConfig:
    """Loads ner_config for the tenant, decrypts the per-tenant LLM key
    if present, and falls back to DEFAULT_NER_CONFIG when no row exists.
    Same pattern as auto_tag's _load_config."""
    try:
        from app.persist import get_pool
    except ImportError:
        return NERConfig.from_dict({})
    pool = await get_pool()
    async with pool.acquire() as conn:
        await conn.execute("SELECT set_config('app.current_tenant', $1, true)", tenant_id)
        row = await conn.fetchrow(
            """SELECT llm_enabled, llm_model, llm_entity_types,
                      llm_batch_size, llm_min_confidence,
                      llm_api_key_encrypted
                 FROM ner_config WHERE tenant_id = $1""",
            tenant_id,
        )
    if not row:
        return NERConfig.from_dict({})
    # Decrypt is best-effort: a stale ciphertext after a key rotation
    # surfaces as "no key" and the LLM call falls back to env vars
    # before silently no-op'ing. Never raises.
    api_key: str | None = None
    if row["llm_api_key_encrypted"]:
        from app.secrets import decrypt_tenant_secret
        api_key = decrypt_tenant_secret(row["llm_api_key_encrypted"])
    return NERConfig.from_dict({
        "llm_enabled": row["llm_enabled"],
        "llm_model": row["llm_model"],
        "llm_entity_types": list(row["llm_entity_types"]),
        "llm_batch_size": row["llm_batch_size"],
        "llm_min_confidence": row["llm_min_confidence"],
        "api_key": api_key,
    })
