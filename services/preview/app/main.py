"""FastAPI entrypoint. Boots the REST API and registers a background task
that runs the NATS consumer.

The same container image serves two roles depending on CMD:
  * `uvicorn app.main:app`              — HTTP API + NATS consumer
  * `celery -A app.worker worker ...`   — background worker

When running as a Celery worker, this module is not imported, so the
consumer startup is a no-op for that role.
"""
from __future__ import annotations

import asyncio
import logging
from contextlib import asynccontextmanager

from fastapi import FastAPI

from app.api.routes import router as previews_router
from app.config import settings
from app.nats_consumer import PreviewConsumer

logging.basicConfig(
    level=getattr(logging, settings.log_level.upper(), logging.INFO),
    format="%(asctime)s %(levelname)s %(name)s %(message)s",
)
log = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(app: FastAPI):
    consumer = PreviewConsumer()
    task = asyncio.create_task(_run_consumer(consumer))
    app.state.consumer = consumer
    app.state.consumer_task = task
    try:
        yield
    finally:
        await consumer.stop()
        task.cancel()
        try:
            await task
        except (asyncio.CancelledError, Exception):
            pass


async def _run_consumer(consumer: PreviewConsumer) -> None:
    try:
        await consumer.start()
        # Block forever — the subscription drives callbacks on its own tasks.
        while True:
            await asyncio.sleep(3600)
    except asyncio.CancelledError:
        raise
    except Exception as e:
        log.exception("nats consumer crashed: %s", e)


app = FastAPI(
    title="SeDoc Preview Service",
    version=settings.service_version,
    lifespan=lifespan,
)
app.include_router(previews_router)


@app.get("/healthz")
def healthz():
    return {"status": "ok", "service": settings.service_name, "version": settings.service_version}


@app.get("/readyz")
def readyz():
    consumer = getattr(app.state, "consumer", None)
    connected = consumer is not None and consumer.nc is not None and consumer.nc.is_connected
    return {"ready": bool(connected)}
