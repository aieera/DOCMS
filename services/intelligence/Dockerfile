FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    libmagic1 poppler-utils ffmpeg \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt .
# Defensive: python:3.12-slim ships with a pip / setuptools / wheel
# combo that's old enough to hit "metadata-generation-failed" on a
# handful of sdist-only deps in requirements.txt (the OCR + NER
# stack). Upgrading them first lets PEP 517 builds work reliably
# rather than depending on whichever PyPI artefact happens to be
# served at build time — saw a clean build today against the same
# source that failed yesterday, so the build was passing by luck.
RUN pip install --no-cache-dir --upgrade pip setuptools wheel
RUN pip install --no-cache-dir -r requirements.txt
# spaCy NER model (services/intelligence/app/models/ner_model.py).
# `python -m spacy download` resolves a model-index URL that 404s on
# spacy 3.7.4, so install the model directly from its pinned release
# wheel instead — this bypasses the broken index and is deterministic.
# en_core_web_sm-3.7.1 is the small (~12 MB) model compatible with
# spacy 3.7.x; enough for the PERSON/GPE/DATE/ORG entities NER uses.
# The heavier en_core_web_trf is opt-in via SEDOC_NER_SPACY_MODEL.
RUN pip install --no-cache-dir \
    https://github.com/explosion/spacy-models/releases/download/en_core_web_sm-3.7.1/en_core_web_sm-3.7.1-py3-none-any.whl

COPY app/ app/

RUN useradd -m appuser && chown -R appuser:appuser /app
USER appuser

# Default CMD is the uvicorn API which exposes /healthz (see app/main.py).
# When compose overrides CMD to run celery, it also overrides the
# healthcheck with `inspect ping`. This HEALTHCHECK therefore fires only
# for the HTTP-API deployment of this image.
HEALTHCHECK --interval=10s --timeout=3s --retries=5 --start-period=30s \
    CMD python -c "import urllib.request,sys; sys.exit(0 if urllib.request.urlopen('http://localhost:8080/healthz', timeout=2).status == 200 else 1)" \
    || exit 1
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8080"]
