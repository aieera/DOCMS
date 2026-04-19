FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    libmagic1 poppler-utils ffmpeg \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
RUN python -m spacy download en_core_web_trf

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
