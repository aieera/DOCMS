"""OCR model loaders — Surya primary, PaddleOCR fallback."""
from __future__ import annotations
import logging

log = logging.getLogger(__name__)

_surya_det = None
_surya_rec = None

def load_surya():
    # surya-ocr 0.4.x renamed the loader symbols: detection and recognition
    # both expose `load_model` / `load_processor` from their own submodules,
    # so we alias them locally to keep the call sites readable. (Pre-0.4
    # exported load_det_model/load_rec_model directly.)
    global _surya_det, _surya_rec
    if _surya_det is None:
        from surya.model.detection.model import load_model as load_det_model, load_processor as load_det_processor
        from surya.model.recognition.model import load_model as load_rec_model
        from surya.model.recognition.processor import load_processor as load_rec_processor
        log.info("loading surya OCR models")
        _surya_det = load_det_model(), load_det_processor()
        _surya_rec = load_rec_model(), load_rec_processor()
    return _surya_det, _surya_rec

def surya_ocr_page(image, languages: list[str] | None = None) -> dict:
    from surya.ocr import run_ocr
    det, rec = load_surya()
    det_model, det_proc = det
    rec_model, rec_proc = rec
    langs = languages or ["en"]
    result = run_ocr([image], [langs], det_model, det_proc, rec_model, rec_proc)
    if not result:
        return {"text": "", "confidence": 0.0, "boxes": []}
    page = result[0]
    text_lines = []
    boxes = []
    confidences = []
    for line in page.text_lines:
        # Defensive: Surya sometimes emits TextLine rows with a
        # degenerate bbox (empty list, fewer than 4 coords, or None)
        # for low-confidence detections. The straight `line.bbox[0]`
        # access used to raise IndexError and crash the whole page
        # — drop the line instead. line.text may also be None on a
        # rejected detection; coerce to "" so the join below is safe.
        bbox = getattr(line, "bbox", None) or []
        if len(bbox) < 4:
            continue
        text = getattr(line, "text", "") or ""
        conf = getattr(line, "confidence", 0.0)
        text_lines.append(text)
        confidences.append(conf)
        boxes.append({
            "x1": bbox[0], "y1": bbox[1],
            "x2": bbox[2], "y2": bbox[3],
            "text": text, "confidence": conf,
        })
    avg_conf = sum(confidences) / len(confidences) if confidences else 0.0
    return {"text": "\n".join(text_lines), "confidence": avg_conf, "boxes": boxes}
