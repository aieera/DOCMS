#!/usr/bin/env python3
"""Assertions over a rendered sedoc chart (`helm template ... | check_rendered.py`).

Reads the multi-document manifest stream on stdin and runs every check named
on the command line (default: all). Exits non-zero, listing each violation,
when any check fails. Needs PyYAML.

Checks:
  ingress-backends  every Ingress backend is the Kong gateway Service or the
                    web Service (C-1); nothing else is reachable from outside.
  migration-secret  no Job/CronJob other than the migrate Job references the
                    migration/owner credential (Secret + key from --values).
"""
import argparse
import sys

import yaml


def load(stream):
    return [d for d in yaml.safe_load_all(stream) if isinstance(d, dict) and d.get("kind")]


def pod_spec(doc):
    kind = doc["kind"]
    spec = doc.get("spec") or {}
    if kind == "CronJob":
        return ((spec.get("jobTemplate") or {}).get("spec") or {}).get("template", {}).get("spec") or {}
    if kind in ("Deployment", "StatefulSet", "DaemonSet", "Job"):
        return (spec.get("template") or {}).get("spec") or {}
    return None


def containers(ps):
    return (ps.get("initContainers") or []) + (ps.get("containers") or [])


def secret_refs(ps):
    """(secret name, key) pairs a pod spec reads, via env or volumes."""
    refs = set()
    for c in containers(ps):
        for e in c.get("env") or []:
            ref = ((e.get("valueFrom") or {}).get("secretKeyRef")) or None
            if ref:
                refs.add((ref.get("name"), ref.get("key"), bool(ref.get("optional"))))
        for ef in c.get("envFrom") or []:
            if ef.get("secretRef"):
                refs.add((ef["secretRef"].get("name"), "*", bool(ef["secretRef"].get("optional"))))
    for v in ps.get("volumes") or []:
        sec = v.get("secret")
        if sec:
            for item in sec.get("items") or [{"key": "*"}]:
                refs.add((sec.get("secretName"), item.get("key"), bool(sec.get("optional"))))
    return refs


def check_migration_secret(docs, args):
    errs = []
    for d in docs:
        if d["kind"] not in ("Job", "CronJob"):
            continue
        ps = pod_spec(d)
        name = d["metadata"]["name"]
        if name.startswith("sedoc-migrate"):
            continue
        for sname, key, _ in secret_refs(ps):
            if sname == args.migration_secret and key in (args.migration_key, "*"):
                errs.append(f"{d['kind']}/{name} reads the migration credential {sname}/{key}")
    return errs


def check_ingress_backends(docs, args):
    services = {d["metadata"]["name"] for d in docs if d["kind"] == "Service"}
    allowed = {s for s in services if s.endswith("-gateway") and "graphql" not in s} | {"sedoc-web"}
    errs = []
    ingresses = [d for d in docs if d["kind"] == "Ingress"]
    for ing in ingresses:
        name = ing["metadata"]["name"]
        spec = ing.get("spec") or {}
        backends = []
        if spec.get("defaultBackend"):
            backends.append(("<default>", spec["defaultBackend"]))
        for rule in spec.get("rules") or []:
            for p in (rule.get("http") or {}).get("paths") or []:
                backends.append((p.get("path"), p.get("backend") or {}))
        for path, be in backends:
            svc = (be.get("service") or {}).get("name")
            if svc not in allowed:
                errs.append(f"Ingress/{name} path {path} -> {svc or be} (only {sorted(allowed)} allowed)")
            elif svc not in services:
                errs.append(f"Ingress/{name} path {path} -> {svc}: no such Service rendered")
    return errs


CHECKS = {
    "ingress-backends": check_ingress_backends,
    "migration-secret": check_migration_secret,
}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("checks", nargs="*", default=sorted(CHECKS))
    ap.add_argument("--migration-secret", default="sedoc-db-credentials")
    ap.add_argument("--migration-key", default="postgres-password")
    args = ap.parse_args()
    docs = load(sys.stdin)
    if not docs:
        print("check_rendered: no manifests on stdin", file=sys.stderr)
        return 2
    failed = False
    for name in args.checks:
        errs = CHECKS[name](docs, args)
        for e in errs:
            print(f"FAIL [{name}] {e}")
        if errs:
            failed = True
        else:
            print(f"ok   [{name}]")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
