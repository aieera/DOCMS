"""OCR model loaders — Surya (printed, primary), PaddleOCR (printed, fallback),
TrOCR (handwriting / ICR)."""
from __future__ import annotations
import logging
import math
import threading

from app.config import settings

log = logging.getLogger(__name__)

_surya_det = None
_surya_rec = None
_paddle = None
_trocr = None

# Loaders are called from Celery tasks AND (when ocr_preload_models is on)
# from a worker-startup thread. Without this the check-then-set on the
# module globals can load Surya twice, doubling ~2 GB of weights.
_load_lock = threading.RLock()


def mean_confidence(values) -> float:
    """Mean of the finite confidences in `values`, 0.0 when there are none.

    Surya's per-line score is `sum(scores)/count(scores != 0)`, which is
    NaN for a detected region that decoded to no tokens (blank line,
    rule, stamp). A single NaN used to poison the whole page average via
    `sum(confidences)/len(confidences)`, so a perfectly-good page was
    persisted with confidence 0.0 while its text and composite quality
    score were fine — that is the "OCR: Good" + "0.0 % char conf"
    contradiction in BUG-16. Drop the non-finite entries instead.
    """
    finite = [
        float(v) for v in values
        if isinstance(v, (int, float)) and math.isfinite(float(v))
    ]
    if not finite:
        return 0.0
    return sum(finite) / len(finite)

# Map our language codes to a PaddleOCR `lang` value. Paddle takes a single
# language; for the ar+en mix we OCR with Surya, the fallback just needs latin.
_PADDLE_LANG = {"en": "en", "ar": "arabic", "fr": "fr", "de": "german", "es": "es"}


def load_paddle(lang: str = "en"):
    global _paddle
    with _load_lock:
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
    avg_conf = mean_confidence(confidences)
    return {"text": "\n".join(text_lines), "confidence": avg_conf, "boxes": boxes}

def load_surya():
    # surya-ocr 0.4.x renamed the loader symbols: detection and recognition
    # both expose `load_model` / `load_processor` from their own submodules,
    # so we alias them locally to keep the call sites readable. (Pre-0.4
    # exported load_det_model/load_rec_model directly.)
    global _surya_det, _surya_rec
    with _load_lock:
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
    avg_conf = mean_confidence(confidences)
    return {"text": "\n".join(text_lines), "confidence": avg_conf, "boxes": boxes}


# ---- TrOCR (handwriting / ICR) -------------------------------------------
#
# TrOCR is a recognition-only transformer (microsoft/trocr-*-handwritten): it
# transcribes a single CROPPED text line, with no detector of its own. So we
# reuse Surya's detector to find line regions, then run TrOCR on each crop —
# yielding the same {text, confidence, boxes} shape every other engine returns,
# which lets the OCR task swap/merge results without special-casing. Lazy-loaded
# like Surya/Paddle so the (large) weights only download on first handwriting
# job, not at import.

def load_trocr():
    global _trocr
    with _load_lock:
        if _trocr is None:
            from transformers import TrOCRProcessor, VisionEncoderDecoderModel
            name = settings.ocr_trocr_model
            log.info("loading TrOCR handwriting model (%s)", name)
            proc = TrOCRProcessor.from_pretrained(name)
            model = VisionEncoderDecoderModel.from_pretrained(name)
            model.eval()
            _trocr = (proc, model)
    return _trocr


def _trocr_line_confidence(model, generated) -> float:
    """Derive a 0..1 confidence from the generation transition scores
    (mean per-token probability). Falls back to a neutral 0.5 if the
    transformers version doesn't expose compute_transition_scores or the
    scores are unavailable — never raises (confidence is advisory)."""
    try:
        import torch
        scores = model.compute_transition_scores(
            generated.sequences, generated.scores, normalize_logits=True,
        )
        # scores are log-probs; drop any -inf padding then mean -> exp.
        finite = scores[torch.isfinite(scores)]
        if finite.numel() == 0:
            return 0.5
        return float(torch.exp(finite.mean()).clamp(0.0, 1.0))
    except Exception:
        return 0.5


def trocr_ocr_page(image, languages: list[str] | None = None) -> dict:
    """Handwriting OCR (ICR). Detects line regions with Surya, transcribes
    each with TrOCR. Returns the standard {text, confidence, boxes} shape;
    boxes carry per-line {x1,y1,x2,y2,text,confidence} like the other engines.
    Raises only if the models can't load — a page with no detected lines
    returns an empty result rather than erroring."""
    import torch
    from surya.detection import batch_text_detection
    det, _rec = load_surya()
    det_model, det_proc = det
    proc, model = load_trocr()

    det_result = batch_text_detection([image], det_model, det_proc)[0]
    regions = []
    for b in getattr(det_result, "bboxes", []) or []:
        bb = getattr(b, "bbox", None)
        if bb and len(bb) >= 4:
            regions.append([float(bb[0]), float(bb[1]), float(bb[2]), float(bb[3])])
    # No detected lines (e.g. a tight single-line crop) → transcribe the
    # whole image so we still produce output.
    if not regions:
        regions = [[0.0, 0.0, float(image.width), float(image.height)]]

    text_lines: list[str] = []
    boxes: list[dict] = []
    confidences: list[float] = []
    for bb in regions:
        x1, y1, x2, y2 = (int(bb[0]), int(bb[1]), int(bb[2]), int(bb[3]))
        if x2 <= x1 or y2 <= y1:
            continue
        crop = image.crop((x1, y1, x2, y2)).convert("RGB")
        pixel_values = proc(images=crop, return_tensors="pt").pixel_values
        with torch.no_grad():
            generated = model.generate(
                pixel_values,
                max_new_tokens=256,
                output_scores=True,
                return_dict_in_generate=True,
            )
        text = proc.batch_decode(generated.sequences, skip_special_tokens=True)[0].strip()
        if not text:
            continue
        conf = _trocr_line_confidence(model, generated)
        text_lines.append(text)
        confidences.append(conf)
        boxes.append({
            "x1": x1, "y1": y1, "x2": x2, "y2": y2,
            "text": text, "confidence": conf,
        })

    avg_conf = mean_confidence(confidences)
    return {"text": "\n".join(text_lines), "confidence": avg_conf, "boxes": boxes}
