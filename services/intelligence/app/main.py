"""FastAPI entrypoint — REST API + NATS consumer for the intelligence service."""
from __future__ import annotations

import asyncio
import logging
from contextlib import asynccontextmanager

from fastapi import FastAPI
from fastapi.responses import Response
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from app.api.routes import router, internal_router, admin_router
from app.config import settings
from app.db.pool import close_pool
from app.nats_consumer import IntelligenceConsumer

logging.basicConfig(
    level=getattr(logging, settings.log_level.upper(), logging.INFO),
    format="%(asctime)s %(levelname)s %(name)s %(message)s",
)
log = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(app: FastAPI):
    consumer = IntelligenceConsumer()
    task = asyncio.create_task(_run_consumer(consumer))
    app.state.consumer = consumer
    try:
        yield
    finally:
        await consumer.stop()
        task.cancel()
        try:
            await task
        except (asyncio.CancelledError, Exception):
            pass
        await close_pool()


async def _run_consumer(consumer: IntelligenceConsumer):
    try:
        await consumer.start()
        while True:
            await asyncio.sleep(3600)
    except asyncio.CancelledError:
        raise
    except Exception as e:
        log.exception("nats consumer crashed: %s", e)


app = FastAPI(title="VaultDMS Intelligence", version=settings.service_version, lifespan=lifespan)
app.include_router(router)
app.include_router(internal_router)
app.include_router(admin_router)


@app.get("/healthz")
def healthz():
    return {"status": "ok", "service": settings.service_name}


@app.get("/metrics")
def metrics() -> Response:
    # Default registry; counters/histograms in app.metrics auto-register.
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)
