"""OCR model loaders — Surya primary, PaddleOCR fallback."""
from __future__ import annotations
import logging

log = logging.getLogger(__name__)

_surya_det = None
_surya_rec = None
_paddle = None

# Map our language codes to a PaddleOCR `lang` value. Paddle takes a single
# language; for the ar+en mix we OCR with Surya, the fallback just needs latin.
_PADDLE_LANG = {"en": "en", "ar": "arabic", "fr": "fr", "de": "german", "es": "es"}


def load_paddle(lang: str = "en"):
    global _paddle
    if _paddle is None:
        from paddleocr import PaddleOCR
        log.info("loading PaddleOCR models (lang=%s)", lang)
        _paddle = PaddleOCR(use_angle_cls=True, lang=lang, show_log=False)
    return _paddle


def paddle_ocr_page(image, languages: list[str] | None = None) -> dict | None:
    """PaddleOCR fallback. Returns the same {text, confidence, boxes} shape as
    surya_ocr_page so the caller can swap results, or None when PaddleOCR isn't
    installed / fails to run (caller then keeps the Surya result)."""
    lang = _PADDLE_LANG.get((languages or ["en"])[0], "en")
    try:
        import numpy as np
        ocr = load_paddle(lang)
    except Exception:
        log.warning("PaddleOCR unavailable; skipping fallback", exc_info=True)
        return None
    try:
        arr = np.array(image.convert("RGB"))
        result = ocr.ocr(arr, cls=True)
    except Exception:
        log.warning("PaddleOCR run failed", exc_info=True)
        return None
    if not result or not result[0]:
        return {"text": "", "confidence": 0.0, "boxes": []}
    text_lines: list[str] = []
    boxes: list[dict] = []
    confidences: list[float] = []
    for line in result[0]:
        try:
            box, (txt, conf) = line[0], line[1]
        except (ValueError, TypeError, IndexError):
            continue
        if not txt:
            continue
        xs = [float(p[0]) for p in box]
        ys = [float(p[1]) for p in box]
        text_lines.append(txt)
        confidences.append(float(conf))
        boxes.append({
            "x1": min(xs), "y1": min(ys), "x2": max(xs), "y2": max(ys),
            "text": txt, "confidence": float(conf),
        })
    avg_conf = sum(confidences) / len(confidences) if confidences else 0.0
    return {"text": "\n".join(text_lines), "confidence": avg_conf, "boxes": boxes}

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
