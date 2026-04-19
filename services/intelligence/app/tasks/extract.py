"""Structured field extraction — regex first, LLM fallback."""
from __future__ import annotations

import json
import logging
import re
import time

from app.worker import celery_app

log = logging.getLogger(__name__)

INVOICE_PATTERNS = {
    "invoice_number": r'(?:Invoice|Inv)[\s#.:]*([A-Z0-9][\w-]{2,20})',
    "total_amount": r'(?:Total|Amount Due|Grand Total|Balance Due)[\s:]*\$?([\d,]+\.?\d{0,2})',
    "date": r'(?:Date|Invoice Date|Issued)[\s:]*(\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|\w+ \d{1,2},? \d{4})',
    "po_number": r'(?:PO|Purchase Order|P\.O\.)[\s#.:]*([A-Z0-9][\w-]{2,20})',
}

INVOICE_LLM_PROMPT = (
    "Extract these fields from the invoice text. Return ONLY valid JSON:\n"
    '{{invoice_number, vendor_name, invoice_date (YYYY-MM-DD), due_date (YYYY-MM-DD), '
    'total_amount (number), currency (ISO 4217 code), tax_amount (number), '
    'line_items: [{{description, quantity, unit_price, amount}}]}}\n\n'
    'Invoice text:\n{text}'
)

CONTRACT_LLM_PROMPT = (
    "Extract these fields from the contract text. Return ONLY valid JSON:\n"
    '{{parties: [], effective_date (YYYY-MM-DD), expiration_date (YYYY-MM-DD), '
    'governing_law, contract_value, payment_terms, auto_renewal (bool), '
    'notice_period_days, termination_clause_summary, key_obligations: []}}\n\n'
    'Contract text:\n{text}'
)


def _regex_extract_invoice(text: str) -> tuple[dict, float]:
    fields = {}
    found = 0
    for name, pattern in INVOICE_PATTERNS.items():
        m = re.search(pattern, text, re.IGNORECASE)
        if m:
            fields[name] = m.group(1).strip()
            found += 1
    confidence = found / len(INVOICE_PATTERNS)
    return fields, confidence


def _llm_extract(text: str, prompt_template: str, tenant_id: str) -> dict:
    from app import llm_gateway
    resp = llm_gateway.completion(
        tenant_id=tenant_id,
        messages=[{"role": "user", "content": prompt_template.format(text=text[:4000])}],
        temperature=0.0,
        max_tokens=1500,
    )
    try:
        return json.loads(resp["content"])
    except json.JSONDecodeError:
        content = resp.get("content", "")
        start = content.find("{")
        end = content.rfind("}") + 1
        if start >= 0 and end > start:
            return json.loads(content[start:end])
        return {}


@celery_app.task(
    name="app.tasks.extract.extract_fields",
    bind=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 2},
    retry_backoff=True,
)
def extract_fields(self, tenant_id: str, document_id: str, version_id: str, document_class: str, text: str):
    start = time.monotonic()
    method = "regex"
    cost_cents = 0.0
    fields = {}

    if document_class == "invoice":
        fields, conf = _regex_extract_invoice(text)
        if conf < 0.8:
            try:
                fields = _llm_extract(text, INVOICE_LLM_PROMPT, tenant_id)
                method = "hybrid" if fields else "llm"
                cost_cents = 2.0
            except Exception as e:
                log.warning("LLM extract failed: %s", e)
    elif document_class == "contract":
        try:
            fields = _llm_extract(text, CONTRACT_LLM_PROMPT, tenant_id)
            method = "llm"
            cost_cents = 2.0
        except Exception as e:
            log.warning("LLM extract failed: %s", e)
    else:
        method = "skipped"

    elapsed_ms = int((time.monotonic() - start) * 1000)
    return {
        "status": "completed",
        "tenant_id": tenant_id,
        "document_id": document_id,
        "version_id": version_id,
        "document_class": document_class,
        "fields": fields,
        "method": method,
        "cost_cents": cost_cents,
        "processing_time_ms": elapsed_ms,
    }
