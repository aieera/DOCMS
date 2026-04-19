"""Email processor — parse EML/MSG, optionally render HTML body to PDF."""
from __future__ import annotations

import email
import logging
import os
from email.policy import default as default_policy

from app.processors import ProcessResult

log = logging.getLogger(__name__)


def _parse_eml(path: str) -> dict:
    with open(path, "rb") as f:
        msg = email.message_from_binary_file(f, policy=default_policy)

    body_text = ""
    body_html = ""
    attachments: list[str] = []

    for part in msg.walk():
        ctype = part.get_content_type()
        disp = (part.get("Content-Disposition") or "").lower()
        if "attachment" in disp:
            name = part.get_filename()
            if name:
                attachments.append(name)
            continue
        if ctype == "text/plain" and not body_text:
            try:
                body_text = part.get_content()
            except Exception:
                body_text = part.get_payload(decode=True).decode(errors="replace") if part.get_payload(decode=True) else ""
        elif ctype == "text/html" and not body_html:
            try:
                body_html = part.get_content()
            except Exception:
                body_html = part.get_payload(decode=True).decode(errors="replace") if part.get_payload(decode=True) else ""

    return {
        "from": str(msg.get("From") or ""),
        "to": str(msg.get("To") or ""),
        "cc": str(msg.get("Cc") or ""),
        "subject": str(msg.get("Subject") or ""),
        "date": str(msg.get("Date") or ""),
        "body_text": body_text,
        "body_html": body_html,
        "attachments": attachments,
    }


def _parse_msg(path: str) -> dict:
    # Lazy import — extract_msg pulls in heavy deps.
    import extract_msg
    m = extract_msg.Message(path)
    try:
        return {
            "from": m.sender or "",
            "to": m.to or "",
            "cc": m.cc or "",
            "subject": m.subject or "",
            "date": str(m.date) if m.date else "",
            "body_text": m.body or "",
            "body_html": m.htmlBody.decode(errors="replace") if isinstance(m.htmlBody, bytes) else (m.htmlBody or ""),
            "attachments": [a.longFilename or a.shortFilename or "" for a in m.attachments],
        }
    finally:
        m.close()


def _render_html_pdf(html: str, subject: str, dest: str) -> bool:
    try:
        from weasyprint import HTML  # lazy — large dep
        HTML(string=html or f"<html><body><h1>{subject}</h1></body></html>").write_pdf(dest)
        return True
    except Exception as e:
        log.warning("weasyprint failed: %s", e)
        return False


def process(input_path: str, workdir: str, mime_type: str | None = None) -> ProcessResult:
    try:
        if mime_type == "application/vnd.ms-outlook" or input_path.lower().endswith(".msg"):
            meta = _parse_msg(input_path)
        else:
            meta = _parse_eml(input_path)
    except Exception as e:
        return ProcessResult(status="failed", error=f"email parse: {type(e).__name__}")

    # Build a text preview from headers + body.
    snippet_parts = [
        f"From: {meta['from']}",
        f"To: {meta['to']}",
        f"Subject: {meta['subject']}",
        f"Date: {meta['date']}",
        "",
        meta.get("body_text") or "",
    ]
    snippet = "\n".join(snippet_parts)[:5000]

    result = ProcessResult(
        status="ready",
        text_preview=snippet,
        language_hint="text",
        metadata={
            "page_count": 1,
            "from": meta["from"],
            "to": meta["to"],
            "cc": meta["cc"],
            "subject": meta["subject"],
            "date": meta["date"],
            "attachment_names": meta["attachments"],
            "attachment_count": len(meta["attachments"]),
            "has_html": bool(meta.get("body_html")),
        },
    )

    # If we have HTML, try a PDF render + thumbnail via pdf_processor.
    if meta.get("body_html"):
        pdf_path = os.path.join(workdir, "email.pdf")
        if _render_html_pdf(meta["body_html"], meta["subject"], pdf_path):
            from app.processors import pdf as pdf_processor
            pdf_result = pdf_processor.process(pdf_path, workdir)
            if pdf_result.status == "ready":
                result.thumbnail_path = pdf_result.thumbnail_path
                result.preview_paths = pdf_result.preview_paths
                result.metadata["page_count"] = pdf_result.metadata.get("page_count", 1)

    return result
