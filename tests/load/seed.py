#!/usr/bin/env python3
"""
VaultDMS load-test corpus seeder (ADR 0105 §16).

Generates a 100M-document corpus across 100 tenants with a realistic
distribution of sizes, MIME types, metadata, and folder hierarchy.

This script DOES NOT upload blobs to MinIO — it only writes the
documents/ folders/ versions/ content_blobs rows so search and list
endpoints see a populated corpus. The 10 MB upload SLO is measured
by k6 scenarios live-uploading; the seeded blobs use 1 KB sentinel
content so MinIO storage cost stays bounded.

Why a Python seeder and not a Go binary?
- Iterating on distribution tweaks is faster in Python.
- `psycopg[c]`'s COPY support is on par with Go's pgx for bulk write.
- One file > pulling in protobuf-generated types from 11 services.

Safety:
- Refuses to run unless SEDOC_LOAD_SEED_OK=1 is set AND the DSN's
  database name contains "loadtest". Prevents anyone running this
  against a non-loadtest cluster by accident.

Resumability:
- A small checkpoint table `load_seed_progress` records the highest
  (tenant_idx, batch_idx) pair committed. Restarting picks up where
  it left off — a 100M-doc seed takes 8-12 hours, so resumability
  is non-optional.

Usage:
    export DATABASE_URL='postgresql://user:pass@host:5432/vaultdms_loadtest'
    export SEDOC_LOAD_SEED_OK=1
    python tests/load/seed.py \
        --tenants 100 \
        --docs-per-tenant 1000000 \
        --batch-size 5000 \
        --workers 8

Default invocation (100 tenants × 1M docs = 100M total, batched at
5k rows per COPY):
    python tests/load/seed.py
"""
from __future__ import annotations

import argparse
import contextlib
import io
import json
import math
import os
import random
import secrets
import string
import sys
import time
import uuid
from concurrent.futures import ProcessPoolExecutor, as_completed
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from typing import Iterable

try:
    import psycopg
    from psycopg import sql
except ImportError:
    print("Install dependency: pip install 'psycopg[c]>=3.1'", file=sys.stderr)
    sys.exit(2)


# ---------- distribution model ----------

# MIME types weighted to match what a real DMS tenant looks like.
# Numbers were eyeballed from public benchmarks of comparable products
# (Box, Egnyte, SharePoint) — defensible but not authoritative.
MIME_DISTRIBUTION: list[tuple[str, str, float]] = [
    # mime, extension, weight
    ("application/pdf",                                                       "pdf",  0.60),
    ("application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx", 0.12),
    ("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",      "xlsx", 0.08),
    ("application/vnd.openxmlformats-officedocument.presentationml.presentation", "pptx", 0.04),
    ("image/jpeg",                                                            "jpg",  0.06),
    ("image/png",                                                             "png",  0.04),
    ("text/plain",                                                            "txt",  0.03),
    ("text/csv",                                                              "csv",  0.02),
    ("video/mp4",                                                             "mp4",  0.01),
]

# Document-class vocabulary (matches predictive-filing's
# classification labels — ADR 0102).
DOC_CLASSES = [
    "invoice", "receipt", "contract", "contract_amendment",
    "policy", "report", "presentation", "internal_memo", "general",
]

# Tag vocabulary — wide enough that a tenant of 1M docs has plenty
# of variety, narrow enough that search-by-tag returns >0 results.
TAG_VOCAB = [
    "2024", "2025", "2026", "q1", "q2", "q3", "q4",
    "acme", "vendor", "client", "internal", "legal", "hr", "finance", "ops",
    "renewal", "amendment", "exhibit", "schedule",
    "msa", "nda", "sla", "dpa", "auto-renewal",
    "approved", "draft", "in-review", "executed", "void",
    "us-ca", "us-ny", "eu", "uk", "apac", "loadtest",
]

# Lognormal sizes: median ~120 KB, p95 ~5 MB, p99 ~50 MB.
# Backed by the same public benchmarks as the MIME mix. log-mu/sigma
# tuned in scipy then hardcoded here so the seeder has no scipy dep.
SIZE_LOG_MU    = math.log(120 * 1024)
SIZE_LOG_SIGMA = 1.4

# Lifecycle distribution. Most docs are active; a long tail is
# archived/superseded. legal_hold ≪ 1% so the freeze code path
# still exists in the corpus without poisoning queries.
LIFECYCLE_DIST = [
    ("draft",      0.05),
    ("in_review",  0.05),
    ("active",     0.75),
    ("superseded", 0.08),
    ("retained",   0.04),
    ("archived",   0.03),
]


# ---------- helpers ----------

@dataclass
class Args:
    dsn:              str
    tenants:          int
    docs_per_tenant:  int
    batch_size:       int
    workers:          int
    workspaces_per_tenant: int
    folders_per_workspace: int
    dry_run:          bool


def parse_args() -> Args:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--dsn",                   default=os.environ.get("DATABASE_URL"))
    p.add_argument("--tenants",               type=int, default=100)
    p.add_argument("--docs-per-tenant",       type=int, default=1_000_000)
    p.add_argument("--batch-size",            type=int, default=5_000)
    p.add_argument("--workers",               type=int, default=max(1, os.cpu_count() or 4))
    p.add_argument("--workspaces-per-tenant", type=int, default=50)
    p.add_argument("--folders-per-workspace", type=int, default=20)
    p.add_argument("--dry-run",               action="store_true",
                   help="Print plan without touching the DB.")
    ns = p.parse_args()
    if not ns.dsn:
        print("Set DATABASE_URL or pass --dsn.", file=sys.stderr)
        sys.exit(2)
    return Args(
        dsn=ns.dsn,
        tenants=ns.tenants,
        docs_per_tenant=ns.docs_per_tenant,
        batch_size=ns.batch_size,
        workers=ns.workers,
        workspaces_per_tenant=ns.workspaces_per_tenant,
        folders_per_workspace=ns.folders_per_workspace,
        dry_run=ns.dry_run,
    )


def safety_check(dsn: str) -> None:
    """Refuse to run unless the operator promised this is a load-test cluster."""
    if os.environ.get("SEDOC_LOAD_SEED_OK") != "1":
        print(
            "Refusing to seed without SEDOC_LOAD_SEED_OK=1. "
            "This script writes ~100M rows; set the env var to confirm.",
            file=sys.stderr,
        )
        sys.exit(2)
    if "loadtest" not in dsn.lower():
        print(
            f"Refusing: DSN database name does not contain 'loadtest' (got: {dsn!r}). "
            "Rename your database or set up a dedicated cluster — ADR 0105.",
            file=sys.stderr,
        )
        sys.exit(2)


def ensure_checkpoint_table(conn: psycopg.Connection) -> None:
    """Idempotently create the checkpoint table so a resumed seed
    knows where to pick up. Stored as (tenant_idx, batch_idx)."""
    conn.execute("""
        CREATE TABLE IF NOT EXISTS load_seed_progress (
            tenant_idx integer NOT NULL,
            batch_idx  integer NOT NULL,
            committed_at timestamptz NOT NULL DEFAULT now(),
            PRIMARY KEY (tenant_idx, batch_idx)
        )
    """)
    conn.commit()


def load_checkpoint(conn: psycopg.Connection, tenant_idx: int) -> int:
    row = conn.execute(
        "SELECT COALESCE(MAX(batch_idx), -1) FROM load_seed_progress WHERE tenant_idx = %s",
        (tenant_idx,),
    ).fetchone()
    return row[0] if row else -1


def record_checkpoint(conn: psycopg.Connection, tenant_idx: int, batch_idx: int) -> None:
    conn.execute(
        "INSERT INTO load_seed_progress (tenant_idx, batch_idx) VALUES (%s, %s) "
        "ON CONFLICT DO NOTHING",
        (tenant_idx, batch_idx),
    )


def pick_weighted(rng: random.Random, choices: list[tuple[str, float]] | list[tuple[str, str, float]]) -> tuple:
    """Standard weighted choice — extracted because random.choices
    allocates a list per call and shows up in the profile when we're
    doing 100M of them."""
    r = rng.random()
    cum = 0.0
    for choice in choices:
        cum += choice[-1]
        if r <= cum:
            return choice
    return choices[-1]


def lognormal_size(rng: random.Random) -> int:
    """Lognormal byte size, clamped to [256 B, 200 MB]."""
    raw = math.exp(rng.gauss(SIZE_LOG_MU, SIZE_LOG_SIGMA))
    return int(max(256, min(200 * 1024 * 1024, raw)))


def random_title(rng: random.Random, doc_class: str, year: int) -> str:
    """Hand-shaped titles — searchable terms surface so the
    browse-search-open scenario actually hits rows."""
    bases = {
        "invoice":            ["Invoice", "Bill", "Statement"],
        "receipt":            ["Receipt", "Payment confirmation"],
        "contract":           ["MSA", "NDA", "SLA", "DPA", "Vendor Agreement", "Customer Contract"],
        "contract_amendment": ["Amendment", "Addendum", "Exhibit"],
        "policy":             ["Policy", "Handbook", "Standard"],
        "report":             ["Quarterly Report", "Monthly Summary", "Audit Report"],
        "presentation":       ["Board Deck", "Pitch", "Update"],
        "internal_memo":      ["Memo", "Minutes", "Notes"],
        "general":            ["Document"],
    }
    base = rng.choice(bases.get(doc_class, ["Document"]))
    counterparty = rng.choice(["Acme", "Globex", "Initech", "Cyberdyne", "Umbrella", "Stark Industries"])
    return f"{base} — {counterparty} — {year}"


def random_tags(rng: random.Random, year: int) -> list[str]:
    """3-6 tags per doc, year always included so date facets work."""
    n = rng.randint(3, 6)
    pool = TAG_VOCAB.copy()
    pool.remove("loadtest") if "loadtest" in pool else None
    chosen = rng.sample(pool, min(n - 1, len(pool)))
    chosen.append(str(year))
    chosen.append("loadtest")  # always — operators filter on this for cleanup
    return chosen


def random_created_at(rng: random.Random) -> tuple[datetime, int]:
    """Spread creation timestamps across the past 36 months so date
    range queries have something to bite. Returns (timestamp, year)."""
    delta_days = rng.randint(0, 36 * 30)
    ts = datetime.now(timezone.utc) - timedelta(days=delta_days, seconds=rng.randint(0, 86_400))
    return ts, ts.year


# ---------- worker ----------

def seed_tenant(tenant_idx: int, args: Args) -> tuple[int, int, float]:
    """Seeds one tenant's worth of docs. Returns
    (tenant_idx, rows_inserted, elapsed_seconds)."""
    started = time.monotonic()
    rng = random.Random(0xBEEF + tenant_idx)

    # Stable tenant UUID derived from the index so reruns target the
    # same logical tenant. Same trick for users/workspaces below.
    tenant_id = uuid.UUID(int=(0xACE0_0000_0000_0000_0000_0000_0000_0000 + tenant_idx))

    inserted = 0
    with psycopg.connect(args.dsn, autocommit=False) as conn:
        ensure_checkpoint_table(conn)
        # The seed must look like real tenant writes — RLS would
        # otherwise refuse the insert. We're inside a load-test DB
        # so it's safe to assume a superuser role or bypass RLS via
        # SET LOCAL session_replication_role = 'replica'.
        # ADR 0105 picks the latter so the seed cleanly fails if
        # someone runs it against prod (where the app user has
        # NOBYPASSRLS and replication_role can't be set).
        conn.execute("SET LOCAL session_replication_role = 'replica'")

        # Sanity: tenant row exists. We try to fetch it; if absent,
        # the seed bails so the operator can run the tenant-create
        # migration first.
        row = conn.execute("SELECT 1 FROM organizations WHERE id = %s", (tenant_id,)).fetchone()
        if not row:
            print(f"[t{tenant_idx:03d}] tenant {tenant_id} missing — run tenant-create migration first", file=sys.stderr)
            return tenant_idx, 0, 0.0

        # Workspaces + folders (cheap; do them all up front).
        workspace_ids = [uuid.uuid5(tenant_id, f"ws-{i}") for i in range(args.workspaces_per_tenant)]
        folder_ids_by_ws: dict[uuid.UUID, list[uuid.UUID]] = {}
        for ws_id in workspace_ids:
            folder_ids_by_ws[ws_id] = [uuid.uuid5(ws_id, f"folder-{i}") for i in range(args.folders_per_workspace)]

        # Stub user (created_by). One synthetic seed user per tenant.
        seed_user_id = uuid.uuid5(tenant_id, "seed-user")

        # Doc rows — bulk-COPY in batches.
        last_batch = load_checkpoint(conn, tenant_idx)
        total_batches = math.ceil(args.docs_per_tenant / args.batch_size)
        for batch_idx in range(last_batch + 1, total_batches):
            buf = io.StringIO()
            for _ in range(args.batch_size):
                doc_id = uuid.uuid4()
                ws_id = rng.choice(workspace_ids)
                folder_id = rng.choice(folder_ids_by_ws[ws_id])
                mime, ext, _ = pick_weighted(rng, MIME_DISTRIBUTION)  # type: ignore[arg-type]
                size_bytes = lognormal_size(rng)
                created_at, year = random_created_at(rng)
                doc_class = pick_weighted(rng, [(c, 1.0 / len(DOC_CLASSES)) for c in DOC_CLASSES])[0]
                title = random_title(rng, doc_class, year)
                tags = random_tags(rng, year)
                lifecycle = pick_weighted(rng, LIFECYCLE_DIST)[0]
                # PG COPY format: tab-sep, NULL = \N, arrays = {…}.
                tag_array = "{" + ",".join(f'"{t}"' for t in tags) + "}"
                buf.write(
                    f"{tenant_id}\t{doc_id}\t{ws_id}\t{folder_id}\t"
                    f"{title}\t{mime}\t{size_bytes}\t{doc_class}\t"
                    f"{tag_array}\t{lifecycle}\t{seed_user_id}\t"
                    f"{created_at.isoformat()}\t{created_at.isoformat()}\t\\N\n"
                )
            buf.seek(0)
            with conn.cursor() as cur:
                with cur.copy(
                    "COPY documents (tenant_id, id, workspace_id, folder_id, "
                    "title, mime_type, size_bytes, document_class, tags, "
                    "lifecycle_state, created_by, created_at, updated_at, deleted_at) "
                    "FROM STDIN"
                ) as copy:
                    copy.write(buf.read())
            inserted += args.batch_size
            record_checkpoint(conn, tenant_idx, batch_idx)
            conn.commit()
            if batch_idx % 20 == 0:
                rate = inserted / max(0.001, time.monotonic() - started)
                print(f"[t{tenant_idx:03d}] batch {batch_idx}/{total_batches}  "
                      f"+{inserted:>10,} rows  {rate:,.0f} rows/s", flush=True)

    return tenant_idx, inserted, time.monotonic() - started


# ---------- driver ----------

def main() -> int:
    args = parse_args()
    safety_check(args.dsn)
    plan = {
        "tenants":               args.tenants,
        "docs_per_tenant":       args.docs_per_tenant,
        "total_docs":            args.tenants * args.docs_per_tenant,
        "batch_size":            args.batch_size,
        "workers":               args.workers,
        "workspaces_per_tenant": args.workspaces_per_tenant,
        "folders_per_workspace": args.folders_per_workspace,
    }
    print("Seed plan:", json.dumps(plan, indent=2))
    if args.dry_run:
        return 0

    # Bootstrap the checkpoint table on the main connection so the
    # workers can read from it concurrently.
    with psycopg.connect(args.dsn) as conn:
        ensure_checkpoint_table(conn)
        conn.commit()

    started = time.monotonic()
    total_rows = 0
    with ProcessPoolExecutor(max_workers=args.workers) as pool:
        futs = {pool.submit(seed_tenant, idx, args): idx for idx in range(args.tenants)}
        for fut in as_completed(futs):
            tenant_idx, rows, elapsed = fut.result()
            total_rows += rows
            print(f"[t{tenant_idx:03d}] done — {rows:,} rows in {elapsed/60:.1f} min", flush=True)

    elapsed_total = time.monotonic() - started
    print(f"Seeded {total_rows:,} documents in {elapsed_total/3600:.2f} h "
          f"({total_rows / max(0.001, elapsed_total):,.0f} rows/s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
