"""3-tier document classification: rules → ML → LLM.

Wave 5 Prompt 5.4 hardening:
- acks_late=True + 3 retries with jitter.
- dedupe via intel_processed_events.
- persistence to document_classifications.
- emits dms.classify.completed.v1 with top_3 + model_version.
- DLQ on terminal failure → dms.dlq.intel_events.classify.<reason>.
"""
from __future__ import annotations

import asyncio
import json
import logging
import re
import time
import uuid
from datetime import datetime, timezone

from app.config import settings
from app.events.publisher import publish_cloudevent
from app.error_classifier import classify_error_reason
from app.intel_dedupe import (
    mark_completed,
    mark_enqueued,
    mark_failed,
    publish_dlq,
)
from app.metrics import (
    classify_documents_total,
    classify_dlq_total,
    classify_duration_seconds,
    classify_method_total,
)
from app.persist import apply_document_class, upsert_classification
from app.worker import celery_app
from app.events.subjects import CLASSIFY_COMPLETED_SUBJECT

log = logging.getLogger(__name__)

CONSUMER = "classify"

RULES: dict[str, list[str]] = {
    "invoice": ["invoice number", "invoice #", "amount due", "bill to", "invoice date"],
    "contract": ["agreement", "hereby", "parties", "governing law", "whereas"],
    "report": ["executive summary", "findings", "recommendations", "analysis"],
    "policy": ["policy number", "coverage", "premium", "effective date", "insured"],
    "resume": ["experience", "education", "skills", "objective", "references"],
    "receipt": ["receipt", "total", "paid", "transaction", "change due"],
    "letter": ["dear", "sincerely", "regards", "to whom it may concern"],
    "memo": ["memorandum", "memo", "from:", "to:", "subject:", "date:"],
    "form": ["please fill", "applicant", "date of birth", "signature"],
    "certificate": ["certify", "awarded", "certificate of", "completion"],
}

LLM_CLASSES = (
    "Invoice, Contract, Report, Policy, Resume, Receipt, Letter, Memo, "
    "Form, Certificate, Correspondence, Specification, Manual, Other"
)


def _tier1_rules(text: str) -> tuple[str, float, list[dict]]:
    lower = text.lower()
    scores: dict[str, int] = {}
    for cls, keywords in RULES.items():
        scores[cls] = sum(1 for kw in keywords if kw in lower)
    if not scores:
        return "other", 0.0, []
    ranked = sorted(scores.items(), key=lambda kv: kv[1], reverse=True)
    top, top_score = ranked[0]
    top3 = [
        {"category": k, "confidence": min(v / 10.0, 0.99), "method": "rules"}
        for k, v in ranked[:3] if v > 0
    ]
    return top, min(top_score / 10.0, 0.99), top3


# transformers gives a model with no id2label mapping the placeholder
# labels LABEL_0, LABEL_1, ... The configured default classifier
# (`distilbert-base-uncased`) is a BASE checkpoint: its sequence
# classification head is randomly initialised, so it emits exactly these
# placeholders with meaningless ~0.5 confidences. Persisting that as a
# document category produced garbage classes like "label_0" and, worse,
# out-scored the rule tier. Treat a placeholder label as "tier-2 has no
# trained model" so the pipeline falls through to the LLM tier.
_PLACEHOLDER_LABEL_RE = re.compile(r"^label[_-]?\d+$", re.IGNORECASE)


def _is_untrained_label(label: str) -> bool:
    return bool(_PLACEHOLDER_LABEL_RE.match((label or "").strip()))


def _tier2_ml(text: str, tenant_id: str = "") -> tuple[str, float, str] | None:
    """Tier-2 classifier. ADR 0060 — when a tenant has a production
    fine-tuned model, use it; otherwise fall through to the default
    classifier so behaviour is unchanged for opt-out tenants.

    Returns None when no usable trained classifier is available (see
    `_is_untrained_label`), so the caller skips the tier entirely instead
    of adopting a placeholder label.
    """
    if tenant_id:
        try:
            from app.tenant_classifier import classify_with_tenant_model
            cls, conf, model_tag = classify_with_tenant_model(tenant_id, text)
            if cls is not None and not _is_untrained_label(cls):
                return cls, conf, model_tag
        except Exception:
            log.exception("tenant model load failed; falling back to default")
    from app.models.classifier import classify
    cls, conf = classify(text)
    if _is_untrained_label(cls):
        log.debug(
            "tier2 ML returned placeholder label %r (%s is an untrained base "
            "checkpoint) — skipping tier", cls, settings.classifier_model,
        )
        return None
    return cls, conf, settings.classifier_model


def _tier3_llm(text: str, tenant_id: str) -> tuple[str, float, str, str]:
    from app import llm_gateway
    prompt = (
        f"Classify this document into exactly ONE category from this list:\n"
        f"[{LLM_CLASSES}]\n\n"
        f"Document text (first 2000 characters):\n{text[:2000]}\n\n"
        f'Respond with ONLY a JSON object: {{"class": "...", "confidence": 0.0-1.0, "reasoning": "one sentence"}}'
    )
    resp = llm_gateway.completion(
        tenant_id=tenant_id,
        messages=[{"role": "user", "content": prompt}],
        temperature=0.0,
        max_tokens=200,
    )
    try:
        parsed = json.loads(resp["content"])
        return (
            parsed.get("class", "Other"),
            float(parsed.get("confidence", 0.5)),
            parsed.get("reasoning", ""),
            settings.default_llm_model,
        )
    except (json.JSONDecodeError, KeyError, ValueError):
        return "Other", 0.3, resp.get("content", ""), settings.default_llm_model


def _build_completed_envelope(
    *,
    tenant_id: str,
    document_id: str,
    version_id: str,
    category: str,
    confidence: float,
    method: str,
    model_version: str,
    top3: list[dict],
    correlation_id: str,
    auto_applied: bool = False,
) -> dict:
    return {
        "specversion": "1.0",
        "id": str(uuid.uuid4()),
        "source": "dms.intelligence.classify",
        "type": CLASSIFY_COMPLETED_SUBJECT,
        "subject": f"version/{version_id}",
        "time": datetime.now(timezone.utc).isoformat(),
        "datacontenttype": "application/json",
        "tenantid": tenant_id,
        "correlationid": correlation_id,
        "data": {
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "category": category,
            "confidence": round(confidence, 3),
            "method": method,
            "model_version": model_version,
            "top_3_categories": top3,
            "document_class": category,  # back-compat alias
            # True when the result cleared classify_auto_apply_threshold
            # AND was written onto documents.document_class. False means
            # "suggestion only — the document is still unclassified".
            "auto_applied": auto_applied,
        },
    }


@celery_app.task(
    name="app.tasks.classify.classify_document",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 3},
    retry_backoff=True,
    retry_backoff_max=60,
    retry_jitter=True,
    soft_time_limit=120,
    time_limit=150,
)
def classify_document(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    text: str,
    event_id: str = "",
    correlation_id: str = "",
):
    start = time.monotonic()

    try:
        asyncio.run(mark_enqueued(
            tenant_id=tenant_id,
            consumer=CONSUMER,
            event_id=event_id,
            document_id=document_id,
            version_id=version_id,
        ))

        cls, conf, top3 = _tier1_rules(text)
        method = "rules"
        model_version = "rules-v1"

        if conf < 0.85:
            try:
                tier2 = _tier2_ml(text, tenant_id)
                if tier2 is not None:
                    ml_cls, ml_conf, ml_version = tier2
                    if ml_conf > conf:
                        cls, conf, method, model_version = ml_cls, ml_conf, "ml", ml_version
            except Exception as e:
                log.warning("tier2 ML failed: %s", e)

        if conf < 0.8:
            try:
                llm_cls, llm_conf, _reasoning, llm_model = _tier3_llm(text, tenant_id)
                if llm_conf > conf:
                    cls, conf, method, model_version = llm_cls.lower(), llm_conf, "llm", llm_model
            except Exception as e:
                log.warning("tier3 LLM failed: %s", e)

        cls = cls.lower()
        classify_method_total.labels(method=method).inc()

        asyncio.run(upsert_classification(
            tenant_id=tenant_id,
            document_id=document_id,
            version_id=version_id,
            category_key=cls,
            confidence=conf,
            method=method,
            model_version=model_version,
            top3=top3,
        ))

        # Human-in-the-loop by design: below the threshold the row above is
        # a *suggestion* and the document stays unclassified. At or above
        # it, promote the result onto the document so the type actually
        # lands instead of every document reading "Unclassified" (BUG-30).
        auto_applied = False
        if conf >= settings.classify_auto_apply_threshold:
            try:
                auto_applied = asyncio.run(apply_document_class(
                    tenant_id=tenant_id,
                    document_id=document_id,
                    version_id=version_id,
                    category_key=cls,
                    confidence=conf,
                ))
            except Exception:
                # The suggestion is already committed; a failed promotion
                # must not fail (and retry) the whole classification.
                log.exception("classify: document_class auto-apply failed")

        envelope = _build_completed_envelope(
            tenant_id=tenant_id,
            document_id=document_id,
            version_id=version_id,
            category=cls,
            confidence=conf,
            method=method,
            model_version=model_version,
            top3=top3,
            correlation_id=correlation_id,
            auto_applied=auto_applied,
        )
        try:
            asyncio.run(publish_cloudevent(
                CLASSIFY_COMPLETED_SUBJECT, envelope, correlation_id=correlation_id
            ))
        except Exception:
            log.exception("classify_completed publish failed")

        asyncio.run(mark_completed(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
        ))
        classify_documents_total.labels(status="completed").inc()
        classify_duration_seconds.observe(time.monotonic() - start)

        log.info(
            "classify.completed",
            extra={
                "tenant_id": tenant_id,
                "document_id": document_id,
                "version_id": version_id,
                "category": cls,
                "confidence": round(conf, 3),
                "method": method,
                "auto_applied": auto_applied,
                "attempt": self.request.retries + 1,
            },
        )
        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "document_class": cls,
            "confidence": round(conf, 3),
            "method": method,
            "model_version": model_version,
            "auto_applied": auto_applied,
            "processing_time_ms": int((time.monotonic() - start) * 1000),
        }
    except Exception as exc:
        is_terminal = self.request.retries >= settings.ocr_max_retries
        if is_terminal:
            reason = classify_error_reason(exc)
            classify_documents_total.labels(status="failed").inc()
            classify_dlq_total.labels(reason=reason).inc()
            try:
                asyncio.run(publish_dlq(
                    consumer=CONSUMER,
                    reason=reason,
                    tenant_id=tenant_id,
                    document_id=document_id,
                    version_id=version_id,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                    attempts=self.request.retries + 1,
                    correlation_id=correlation_id,
                ))
                asyncio.run(mark_failed(
                    tenant_id=tenant_id,
                    consumer=CONSUMER,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                ))
            except Exception:
                log.exception("classify terminal DLQ publish failed")
        raise
