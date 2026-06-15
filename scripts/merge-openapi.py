#!/usr/bin/env python3
"""Merge the hand-maintained OpenAPI fragment into the buf-generated swagger.

The grpc-gateway openapiv2 plugin only documents proto RPCs that carry
google.api.http annotations (today: services/document's gateway routes). The
many custom http.ServeMux handlers — external-key upsert, ingestion, review
queue, storage upload proxy, bulk, download/decrypt-stream — are not proto RPCs
and so never appear in the generated spec.

This script folds proto/openapi/handwired.swagger.json into
proto/gen/openapi/sedoc.swagger.json so the published spec is complete. It runs
from `make proto-gen` AFTER buf generate, so the merge survives every
regeneration (buf overwrites sedoc.swagger.json, then this re-applies the
hand-wired routes). Idempotent.

Usage: python3 scripts/merge-openapi.py
"""
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GENERATED = os.path.join(ROOT, "proto", "gen", "openapi", "sedoc.swagger.json")
HANDWIRED = os.path.join(ROOT, "proto", "openapi", "handwired.swagger.json")


def load(path):
    with open(path) as f:
        return json.load(f)


def merge_section(dst, src, key):
    """Union dst[key] with src[key] (dicts). src wins on conflict but conflicts
    are reported so a proto route and a hand-wired route can't silently shadow."""
    out = dict(dst.get(key, {}))
    for k, v in (src.get(key) or {}).items():
        if k in out and out[k] != v:
            print(f"  ! overriding {key}[{k}] from handwired", file=sys.stderr)
        out[k] = v
    if out:
        dst[key] = out


def merge_tags(dst, src):
    by_name = {t.get("name"): t for t in dst.get("tags", []) if isinstance(t, dict)}
    for t in src.get("tags", []) or []:
        by_name.setdefault(t.get("name"), t)
    if by_name:
        dst["tags"] = list(by_name.values())


def main():
    if not os.path.exists(GENERATED):
        print(f"generated spec not found: {GENERATED} (run `buf generate` first)", file=sys.stderr)
        return 1
    gen = load(GENERATED)
    hand = load(HANDWIRED)

    before = len(gen.get("paths", {}))

    # A sensible published title/description — the generated one is just the
    # first proto filename ("sedoc/v1/common.proto").
    gen["info"] = {
        "title": "SeDoc API",
        "version": "v1",
        "description": "Multi-tenant enterprise Document Management System. Proto-generated gateway routes plus the hand-wired HTTP surface (see the hand-wired tags + INTEGRATION.md for the ERP integration contracts).",
    }

    for key in ("paths", "definitions", "securityDefinitions", "parameters", "responses"):
        merge_section(gen, hand, key)
    merge_tags(gen, hand)

    with open(GENERATED, "w") as f:
        json.dump(gen, f, indent=2, sort_keys=True)
        f.write("\n")

    after = len(gen["paths"])
    print(f"merged hand-wired OpenAPI: {before} -> {after} paths "
          f"(+{after - before} hand-wired) in {os.path.relpath(GENERATED, ROOT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
