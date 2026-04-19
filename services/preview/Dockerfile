FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    libreoffice-core libreoffice-writer libreoffice-calc libreoffice-impress \
    poppler-utils ffmpeg libmagic1 \
    fonts-dejavu fonts-liberation \
    libcairo2 libpango-1.0-0 libpangoft2-1.0-0 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app/ app/

RUN useradd -m appuser && chown -R appuser:appuser /app
USER appuser

# Default command runs the API. Override with `celery -A app.worker worker`
# to run as a worker.
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8080"]
