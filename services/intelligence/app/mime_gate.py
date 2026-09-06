"""Single source of truth for which mime types the extraction pipeline accepts.

Both the upload-event consumer (nats_consumer._on_uploaded) and the OCR task
(tasks.ocr) import from here. They used to hold separate copies of OCR_MIMES;
the consumer's copy never grew the text-mime branch, so directly-uploaded
text files were ACK'd and skipped — never extracted, embedded, or indexed
(QA SD-02), while the viewer showed "OCR Pending" forever (QA SD-13).
No imports on purpose: keep this module dependency-free and unit-testable.
"""

# Mimes that go through a recognition pass (rasterise + OCR).
OCR_MIMES = {
    "application/pdf", "image/jpeg", "image/png", "image/tiff",
    "image/webp", "image/gif", "image/bmp",
}

# application/* types whose bytes are already text.
TEXTUAL_APPLICATION_MIMES = {
    "application/json", "application/xml", "application/x-ndjson",
    "application/csv", "application/x-yaml",
}


def is_text_mime(mime: str) -> bool:
    """True for already-textual files (plain text, markdown, csv, json, …).
    These don't need OCR — the bytes ARE the text — but they DO need their
    content extracted so it flows to lang_detect + embed + index."""
    if mime.startswith("text/"):
        return True
    return mime in TEXTUAL_APPLICATION_MIMES


def is_extractable_mime(mime: str) -> bool:
    """Gate for the extraction pipeline: OCR-able, or already textual."""
    return mime in OCR_MIMES or is_text_mime(mime)
