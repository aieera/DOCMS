"""Per-class / per-tenant structured-extraction profiles.

A profile says *which* business fields to pull for a document_class and *how*
to find each one (the labelled-field regexes). The regexes + sensible defaults
live here in code (never tenant-supplied — a tenant choosing arbitrary regex
would be an ReDoS / injection surface); a row in the per-tenant
`extraction_profiles` table only SELECTS a subset of a class's known fields (or
disables extraction for that class).

Each class designates one field as its `doc_number` — the stable business key
(invoice_number, po_number, …) that the routing step (Workstream 3) matches
uploads against existing documents.
"""
from __future__ import annotations

from dataclasses import dataclass, field as dc_field
from typing import Optional

from app.db.pool import get_pool

# Shared sub-patterns -------------------------------------------------------
_DATE = r'(\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|\d{4}-\d{2}-\d{2}|\w+ \d{1,2},? \d{4})'
_AMOUNT = r'\$?\s*([\d,]+\.\d{2}|[\d,]{2,})'
_NUMBER = r'([A-Z0-9][\w\-/]{2,30})'
_NAME = r'([A-Z][A-Za-z0-9 .,&\'-]{2,60})'


@dataclass(frozen=True)
class FieldSpec:
    """One extractable field: its key plus the ordered regexes to try.

    The first pattern that matches wins; group(1) is the captured value.
    `is_doc_number` marks the class's primary business key.
    """
    key: str
    patterns: tuple[str, ...]
    is_doc_number: bool = False


def _num(label: str) -> tuple[str, ...]:
    return (rf'(?:{label})\s*(?:No\.?|Number|#|:)?\s*[:#]?\s*{_NUMBER}',)


def _date(label: str) -> tuple[str, ...]:
    return (rf'(?:{label})\s*:?\s*{_DATE}',)


def _amount(label: str) -> tuple[str, ...]:
    return (rf'(?:{label})\s*:?\s*{_AMOUNT}',)


def _name(label: str) -> tuple[str, ...]:
    return (rf'(?:{label})\s*:?\s*\n?\s*{_NAME}',)


# Built-in profiles keyed by normalized document_class -----------------------
DEFAULT_PROFILES: dict[str, list[FieldSpec]] = {
    "invoice": [
        FieldSpec("invoice_number", _num(r'Invoice|Inv'), is_doc_number=True),
        FieldSpec("date", _date(r'Invoice Date|Date|Issued')),
        FieldSpec("total", _amount(r'Total|Amount Due|Grand Total|Balance Due')),
        FieldSpec("customer_name", _name(r'Bill\s*To|Customer|Sold\s*To')),
    ],
    "purchase_order": [
        FieldSpec("po_number", _num(r'Purchase Order|PO|P\.O\.'), is_doc_number=True),
        FieldSpec("date", _date(r'Order Date|PO Date|Date')),
        FieldSpec("total", _amount(r'Total|Order Total|Grand Total')),
        FieldSpec("vendor_name", _name(r'Vendor|Supplier|Ship\s*To')),
    ],
    "sales_order": [
        FieldSpec("so_number", _num(r'Sales Order|SO|S\.O\.'), is_doc_number=True),
        FieldSpec("date", _date(r'Order Date|SO Date|Date')),
        FieldSpec("total", _amount(r'Total|Order Total|Grand Total')),
        FieldSpec("customer_name", _name(r'Customer|Bill\s*To|Sold\s*To')),
    ],
    "delivery_order": [
        FieldSpec("do_number", _num(r'Delivery Order|DO|Delivery Note|D\.O\.'), is_doc_number=True),
        FieldSpec("date", _date(r'Delivery Date|DO Date|Date')),
        FieldSpec("customer_name", _name(r'Customer|Deliver\s*To|Ship\s*To')),
    ],
    "quote": [
        FieldSpec("quote_number", _num(r'Quote|Quotation|Estimate|RFQ'), is_doc_number=True),
        FieldSpec("date", _date(r'Quote Date|Date|Valid From')),
        FieldSpec("total", _amount(r'Total|Estimated Total|Grand Total')),
        FieldSpec("customer_name", _name(r'Customer|Prepared\s*For|Bill\s*To')),
    ],
    "receipt": [
        FieldSpec("receipt_number", _num(r'Receipt|Transaction|Ref'), is_doc_number=True),
        FieldSpec("date", _date(r'Date|Transaction Date')),
        FieldSpec("total", _amount(r'Total|Amount Paid|Paid')),
    ],
}

# Aliases — the classifier emits lowercase class names; map common synonyms /
# abbreviations onto the canonical profile key.
CLASS_ALIASES: dict[str, str] = {
    "po": "purchase_order",
    "purchaseorder": "purchase_order",
    "purchase-order": "purchase_order",
    "so": "sales_order",
    "salesorder": "sales_order",
    "sales-order": "sales_order",
    "do": "delivery_order",
    "deliveryorder": "delivery_order",
    "delivery-order": "delivery_order",
    "delivery_note": "delivery_order",
    "quotation": "quote",
    "estimate": "quote",
    "bill": "invoice",
}


def normalize_class(document_class: str) -> str:
    key = (document_class or "").strip().lower().replace(" ", "_")
    return CLASS_ALIASES.get(key.replace("_", ""), CLASS_ALIASES.get(key, key))


def default_profile(document_class: str) -> Optional[list[FieldSpec]]:
    """Return the built-in field specs for a class, or None if no profile
    exists (extraction is skipped for that class)."""
    return DEFAULT_PROFILES.get(normalize_class(document_class))


async def _load_tenant_override(tenant_id: str, normalized_class: str) -> Optional[dict]:
    """Read the per-tenant override row, or None when absent."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        await conn.execute("SELECT set_config('app.current_tenant', $1, true)", tenant_id)
        row = await conn.fetchrow(
            """
            SELECT fields, enabled FROM extraction_profiles
             WHERE tenant_id = $1 AND document_class = $2
            """,
            tenant_id, normalized_class,
        )
    if row is None:
        return None
    return {"fields": list(row["fields"] or []), "enabled": bool(row["enabled"])}


async def resolve_profile(tenant_id: str, document_class: str) -> Optional[list[FieldSpec]]:
    """Resolve the effective field specs for (tenant, class).

    Defaults come from DEFAULT_PROFILES; a tenant row may disable extraction
    for the class (enabled=False → None) or narrow it to a chosen subset of
    the class's known fields. An unknown class with no override → None.
    """
    normalized = normalize_class(document_class)
    base = DEFAULT_PROFILES.get(normalized)
    if base is None:
        return None
    try:
        override = await _load_tenant_override(tenant_id, normalized)
    except Exception:
        override = None  # config read must never block extraction
    if override is None:
        return base
    if not override["enabled"]:
        return None
    chosen = set(override["fields"])
    if not chosen:
        return base
    # Keep order from the default profile; always retain the doc_number field
    # so routing keeps a business key even if a tenant trims the field set.
    return [fs for fs in base if fs.key in chosen or fs.is_doc_number]
