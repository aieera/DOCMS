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
# spacy en_core_web_trf is only used by services/intelligence/app/
# models/ner_model.py. Build-time download was breaking image build
# because spacy 3.7.4's model-index URL resolves to a 404. OCR /
# classify / embed don't need this; NER falls back to a runtime
# download on first invocation. Re-enable when spacy upgrades and
# the model URL resolves correctly.
# RUN python -m spacy download en_core_web_trf

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
