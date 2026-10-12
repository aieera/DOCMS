#!/usr/bin/env python3
"""Assertions over a rendered sedoc chart (`helm template ... | check_rendered.py`).

Reads the multi-document manifest stream on stdin and runs every check named
on the command line (default: all). Exits non-zero, listing each violation,
when any check fails. Needs PyYAML.

Checks:
  ingress-backends  every Ingress backend is the Kong gateway Service or the
                    web Service (C-1); nothing else is reachable from outside.
  secret-alignment  with ExternalSecrets rendered: every required secret key
                    a pod reads is produced by an ExternalSecret, and every
                    ExternalSecret key is read by some pod (R-18). Skipped
                    when no ExternalSecret is rendered.
  restricted        every workload this chart (not a subchart) renders meets
                    the `restricted` Pod Security Standard and has a
                    read-only root filesystem, and no image uses :latest
                    (R-15).
  migration-secret  no Job/CronJob other than the migrate Job references the
                    migration/owner credential (Secret + key from --values).
"""
import argparse
import sys

import yaml


def load(stream):
    """Documents with their `# Source:` path (helm template prints it) in
    doc["__source__"], so subchart objects (…/charts/…) can be told apart."""
    docs, chunk = [], []

    def flush():
        text = "".join(chunk)
        src = ""
        for line in chunk:
            if line.startswith("# Source: "):
                src = line[len("# Source: "):].strip()
                break
        for d in yaml.safe_load_all(text):
            if isinstance(d, dict) and d.get("kind"):
                d["__source__"] = src
                docs.append(d)

    for line in stream:
        if line.startswith("---"):
            flush()
            chunk = []
        else:
            chunk.append(line)
    flush()
    return docs


def ours(doc):
    return "/charts/" not in doc.get("__source__", "")


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


def check_secret_alignment(docs, args):
    produced = set()
    for d in docs:
        if d["kind"] == "ExternalSecret":
            target = ((d.get("spec") or {}).get("target") or {}).get("name") or d["metadata"]["name"]
            for item in (d.get("spec") or {}).get("data") or []:
                produced.add((target, item.get("secretKey")))
    if not produced:
        return []
    read_required, read_any = set(), set()
    for d in docs:
        ps = pod_spec(d)
        if not ps:
            continue
        for name, key, optional in secret_refs(ps):
            read_any.add((name, key))
            if not optional:
                read_required.add((name, key))
    errs = []
    for name, key in sorted(read_required - produced):
        errs.append(f"pods read {name}/{key} but no ExternalSecret produces it")
    for name, key in sorted(produced - read_any):
        errs.append(f"ExternalSecret produces {name}/{key} but no pod reads it")
    return errs


# Writable root by design (PSS `restricted` does not require read-only root):
# Python/HF/pip caches, LibreOffice/Ghostscript scratch, the Node runtime.
RW_ROOT_OK = {
    "sedoc-collaboration",
    "sedoc-intelligence-api", "sedoc-intelligence-worker",
    "sedoc-preview-api", "sedoc-preview-worker",
}


def check_restricted(docs, args):
    errs = []
    for d in docs:
        ps = pod_spec(d)
        if not ps or not ours(d):
            continue
        name = f"{d['kind']}/{d['metadata']['name']}"
        pod_sc = ps.get("securityContext") or {}
        if ps.get("hostNetwork") or ps.get("hostPID") or ps.get("hostIPC"):
            errs.append(f"{name}: host namespaces")
        for c in containers(ps):
            sc = c.get("securityContext") or {}
            cname = f"{name} container {c.get('name')}"
            if not (sc.get("runAsNonRoot") or pod_sc.get("runAsNonRoot")):
                errs.append(f"{cname}: runAsNonRoot not set")
            if sc.get("allowPrivilegeEscalation") is not False:
                errs.append(f"{cname}: allowPrivilegeEscalation must be false")
            if "ALL" not in ((sc.get("capabilities") or {}).get("drop") or []):
                errs.append(f"{cname}: capabilities.drop must include ALL")
            seccomp = (sc.get("seccompProfile") or pod_sc.get("seccompProfile") or {}).get("type")
            if seccomp not in ("RuntimeDefault", "Localhost"):
                errs.append(f"{cname}: seccompProfile RuntimeDefault/Localhost required")
            if sc.get("privileged"):
                errs.append(f"{cname}: privileged")
            if args.read_only_root and not sc.get("readOnlyRootFilesystem") \
                    and d["metadata"]["name"] not in RW_ROOT_OK:
                errs.append(f"{cname}: readOnlyRootFilesystem not set")
            image = str(c.get("image", ""))
            if image.endswith(":latest") or (":" not in image.rsplit("/", 1)[-1] and "@" not in image):
                errs.append(f"{cname}: image {image!r} is unpinned")
    return errs


CHECKS = {
    "restricted": check_restricted,
    "secret-alignment": check_secret_alignment,
    "ingress-backends": check_ingress_backends,
    "migration-secret": check_migration_secret,
}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("checks", nargs="*", default=sorted(CHECKS))
    ap.add_argument("--read-only-root", action=argparse.BooleanOptionalAction, default=True,
                    help="also require readOnlyRootFilesystem (default on)")
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
