#!/usr/bin/env python3
"""Assert that every metric an alert rule or dashboard queries is emitted.

O-2 / O-3: the rules and dashboards under deploy/monitoring queried a dozen
metrics nothing emits and filtered on labels the emitted metrics do not
carry, so the alerts could never fire. This script is the guard.

It collects the emitted metric names (and their label names) from
  * Go:      prometheus.New{Counter,Gauge,Histogram,Summary}[Vec] declarations
             in packages that are actually imported somewhere (a package
             nobody imports registers nothing — pkg/metrics today);
  * Python:  prometheus_client Counter/Gauge/Histogram/Summary declarations
             that are recorded in the process serving /metrics: the FastAPI
             app (app/main.py and what it imports, minus app.tasks). Celery
             task modules run in worker processes that expose no /metrics,
             so a metric only recorded there is never scraped;
  * recording rules in the rule files;
  * EXPORTERS below: third-party exporters the deployment runs, each with
    the condition under which it exists;
and then checks, for every PromQL expression in
deploy/monitoring/alerts/*.yml and deploy/monitoring/dashboards/*.json:
  1. every metric referenced is emitted;
  2. every label matcher on a metric uses a label that metric carries (or
     a target label Prometheus attaches at scrape time), and — where every
     Go call site passes string literals (WithLabelValues / With) — a value
     (or regex) the code can actually produce;
and, for every alerting rule:
  3. labels.severity is one of the tiers alertmanager.yml routes
     (p0..p3), labels.service is set and annotations.summary exists;
  4. alertmanager.yml has a route for every severity tier, no ${...}
     placeholders and no inline receiver credentials (O-1: *_file only).

Usage: python3 deploy/monitoring/check-metrics.py [--repo DIR]
Exit status 1 lists every violation. Needs PyYAML.
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import re
import sys

import yaml

SEVERITIES = ("p0", "p1", "p2", "p3")

# Labels Prometheus (or the operator's ServiceMonitor/PodMonitor relabeling)
# attaches to every scraped series, plus the histogram/summary labels.
TARGET_LABELS = {
    "job", "instance", "namespace", "pod", "service", "container",
    "endpoint", "node", "cluster", "le", "quantile",
}

# Target labels a service metric can collide with. The services'
# ServiceMonitors (templates/<svc>/servicemonitor.yaml) do not set
# honorLabels, so on Kubernetes Prometheus renames the metric's own label
# to exported_<label> and `<label>` becomes the target's value; the compose
# scrape config has no such target label, so there the metric's own label
# survives. An aggregation that keeps one must keep both.
COLLIDING_LABELS = {"service", "namespace", "pod", "container", "job", "instance", "endpoint"}
AGG_LIST = re.compile(r"\b(?:by|without)\s*\(([^)]*)\)")

# Third-party exporters: metric -> (label names or None for "any", source).
EXPORTERS: dict[str, tuple[set[str] | None, str]] = {
    "up": (None, "Prometheus scrape status"),
    # Kong 3.x prometheus plugin (deploy/gateway/kong.yaml, global plugin;
    # status_code_metrics + latency_metrics on). Status listener :8100.
    "kong_http_requests_total": (
        {"service", "route", "code", "source", "workspace", "consumer"},
        "Kong prometheus plugin",
    ),
    "kong_request_latency_ms": ({"service", "route", "workspace"}, "Kong prometheus plugin"),
    "kong_upstream_latency_ms": ({"service", "route", "workspace"}, "Kong prometheus plugin"),
    "kong_kong_latency_ms": ({"service", "route", "workspace"}, "Kong prometheus plugin"),
    # kube-state-metrics / node-exporter (kube-prometheus-stack). Only the
    # Kubernetes rule file (alerts/kubernetes.yml) may use these.
    "kube_pod_container_status_restarts_total": (
        {"namespace", "pod", "container", "uid"}, "kube-state-metrics"),
    "kube_job_status_failed": ({"namespace", "job_name", "reason"}, "kube-state-metrics"),
    "kube_job_owner": (
        {"namespace", "job_name", "owner_kind", "owner_name", "owner_is_controller"},
        "kube-state-metrics"),
    "node_filesystem_size_bytes": ({"device", "fstype", "mountpoint"}, "node-exporter"),
    "node_filesystem_avail_bytes": ({"device", "fstype", "mountpoint"}, "node-exporter"),
    # Postgres: bitnami postgresql.metrics.enabled (postgres_exporter) or the
    # CloudNativePG PodMonitor (postgresql.useOperator).
    "pg_up": (set(), "postgres_exporter (postgresql.metrics.enabled)"),
    "cnpg_collector_up": ({"cluster"}, "CloudNativePG PodMonitor"),
}
# k6 remote-write output during load-test campaigns (tests/load); only the
# load-test dashboard reads these.
EXPORTER_PREFIXES = {"k6_": "k6 prometheus remote-write (load-test campaigns)"}

PROMQL_FUNCS_AND_KEYWORDS = {
    # aggregations
    "sum", "min", "max", "avg", "group", "stddev", "stdvar", "count",
    "count_values", "bottomk", "topk", "quantile", "limitk", "limit_ratio",
    # keywords / modifiers
    "by", "without", "on", "ignoring", "group_left", "group_right", "and",
    "or", "unless", "bool", "offset", "atan2", "inf", "nan",
}
LABEL_LIST_KEYWORDS = {"by", "without", "on", "ignoring", "group_left", "group_right"}

GO_DECL = re.compile(
    r"New(Counter|Gauge|Histogram|Summary)(Vec)?\(\s*(?:prometheus\.)?(?:Counter|Gauge|Histogram|Summary)Opts\{"
)
PY_DECL = re.compile(
    r"\b(Counter|Gauge|Histogram|Summary)\(\s*\"(?P<name>[a-zA-Z_:][a-zA-Z0-9_:]*)\"\s*,"
    r"(?P<rest>.*?)\)",
    re.S,
)
MATCHER = re.compile(r'([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|!~|!=|=)\s*"((?:[^"\\]|\\.)*)"')


class Metric:
    def __init__(self, name: str, kind: str, labels: set[str] | None, source: str,
                 values: dict[str, set[str] | None] | None = None):
        self.name, self.kind, self.labels, self.source = name, kind, labels, source
        # label -> the complete set of values the code can produce, or None
        # when any call site passes a non-literal (value set unknown).
        self.values = values or {}


def call_args(src: str, open_idx: int) -> tuple[list[str], int]:
    """Top-level comma-separated arguments of the call whose '(' is at open_idx."""
    depth, start, args, i = 0, open_idx + 1, [], open_idx
    in_str = False
    while i < len(src):
        c = src[i]
        if in_str:
            if c == "\\":
                i += 2
                continue
            if c == '"':
                in_str = False
        elif c == '"':
            in_str = True
        elif c in "([{":
            depth += 1
        elif c in ")]}":
            depth -= 1
            if depth == 0:
                args.append(src[start:i])
                return [a.strip() for a in args if a.strip()], i
        elif c == "," and depth == 1:
            args.append(src[start:i])
            start = i + 1
        i += 1
    return [], len(src)


GO_STRING = re.compile(r'^"((?:[^"\\]|\\.)*)"$')


def go_label_values(pkg_src: str, var: str, labels: list[str]) -> dict[str, set[str] | None]:
    """Value sets per label from var.WithLabelValues(...) / var.With(prometheus.Labels{...})."""
    seen: dict[str, set[str] | None] = {}

    def add(label: str, arg: str) -> None:
        lit = GO_STRING.match(arg)
        if lit is None:
            seen[label] = None
        elif seen.get(label, set()) is not None:
            seen.setdefault(label, set()).add(lit.group(1))

    for m in re.finditer(r"\b" + re.escape(var) + r"\.(WithLabelValues|With)\(", pkg_src):
        args, _ = call_args(pkg_src, m.end() - 1)
        if m.group(1) == "WithLabelValues":
            for label, arg in zip(labels, args):
                add(label, arg)
        else:
            body = args[0] if args else ""
            for k, v in re.findall(r'"([^"]+)"\s*:\s*([^,}]+)', body):
                add(k, v.strip())
    return {k: v for k, v in seen.items() if k in labels}


def balanced_close(src: str, open_idx: int) -> int:
    """Index of the '}' matching the '{' at open_idx."""
    depth = 0
    for i in range(open_idx, len(src)):
        if src[i] == "{":
            depth += 1
        elif src[i] == "}":
            depth -= 1
            if depth == 0:
                return i
    raise ValueError("unbalanced braces")


UNRESOLVED: list[str] = []


def go_import_paths(repo: str) -> set[str]:
    """Every import path that appears in a non-test Go file."""
    paths: set[str] = set()
    for f in glob.glob(os.path.join(repo, "**", "*.go"), recursive=True):
        if f.endswith("_test.go") or "/node_modules/" in f:
            continue
        with open(f, encoding="utf-8", errors="replace") as fh:
            paths.update(re.findall(r'"(github\.com/aieera/sedoc/[^"]+)"', fh.read()))
    return paths


def collect_go(repo: str) -> dict[str, Metric]:
    imported = go_import_paths(repo)
    out: dict[str, Metric] = {}
    for top in ("services", "pkg", "cmd"):
        for f in glob.glob(os.path.join(repo, top, "**", "*.go"), recursive=True):
            if f.endswith("_test.go") or "/gen/" in f:
                continue
            src = open(f, encoding="utf-8", errors="replace").read()
            if "client_golang/prometheus" not in src:
                continue
            pkg_dir = os.path.relpath(os.path.dirname(f), repo)
            is_main = re.search(r"^package main\b", src, re.M) is not None
            if not is_main and not any(p.endswith("/" + pkg_dir) for p in imported):
                continue  # nobody imports it: its promauto vars never register
            pkg_src = "".join(
                open(g, encoding="utf-8", errors="replace").read()
                for g in glob.glob(os.path.join(os.path.dirname(f), "*.go"))
                if not g.endswith("_test.go")
            )
            for m in GO_DECL.finditer(src):
                close = balanced_close(src, m.end() - 1)
                opts = src[m.end() : close]
                name = re.search(r'Name:\s*"([^"]+)"', opts)
                if not name:
                    continue
                # Namespace + Subsystem + Name, joined with "_" like
                # prometheus.BuildFQName.
                parts = []
                for field in ("Namespace", "Subsystem"):
                    if re.search(field + r":", opts):
                        lit = re.search(field + r':\s*"([^"]*)"', opts)
                        if lit is None:
                            UNRESOLVED.append(f"{pkg_dir}: {name.group(1)} has a non-literal {field}; cannot compose its name")
                        elif lit.group(1):
                            parts.append(lit.group(1))
                full = "_".join(parts + [name.group(1)])
                labels: list[str] = []
                if m.group(2):  # ...Vec(opts, []string{...})
                    lab = re.match(r"\s*,\s*\[\]string\{([^}]*)\}", src[close + 1 :])
                    if lab:
                        labels = re.findall(r'"([^"]+)"', lab.group(1))
                var = re.search(r"(\w+)\s*=\s*(?:\w+\.)?$", src[: m.start()])
                values = go_label_values(pkg_src, var.group(1), labels) if var and labels else {}
                out[full] = Metric(full, m.group(1).lower(), set(labels), pkg_dir, values)
    return out


def served_python_modules(app_dir: str) -> set[str]:
    """Files reachable by imports from app/main.py, excluding app.tasks.*."""
    main = os.path.join(app_dir, "main.py")
    if not os.path.exists(main):
        return set()
    seen: set[str] = set()
    todo = [main]
    while todo:
        f = todo.pop()
        if f in seen:
            continue
        seen.add(f)
        src = open(f, encoding="utf-8", errors="replace").read()
        for mod in re.findall(r"^\s*(?:from|import)\s+(app(?:\.[a-zA-Z_][a-zA-Z0-9_]*)*)", src, re.M):
            if mod == "app.tasks" or mod.startswith("app.tasks."):
                continue
            rel = mod.split(".")[1:]
            if not rel:
                continue
            base = os.path.join(app_dir, *rel)
            for cand in (base + ".py", os.path.join(base, "__init__.py")):
                if os.path.exists(cand):
                    todo.append(cand)
    return seen


def collect_python(repo: str) -> dict[str, Metric]:
    out: dict[str, Metric] = {}
    for app_dir in glob.glob(os.path.join(repo, "services", "*", "app")):
        served = served_python_modules(app_dir)
        decls: dict[str, tuple[str, str, Metric]] = {}  # var -> (file, name, metric)
        for f in glob.glob(os.path.join(app_dir, "**", "*.py"), recursive=True):
            src = open(f, encoding="utf-8", errors="replace").read()
            if "prometheus_client" not in src:
                continue
            for m in PY_DECL.finditer(src):
                var = re.search(r"([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*$", src[: m.start()])
                kind = m.group(1).lower()
                name = m.group("name")
                lab = re.search(r"\[([^\]]*)\]", m.group("rest"))
                labels = set(re.findall(r'"([^"]+)"', lab.group(1))) if lab else set()
                if kind == "counter" and not name.endswith("_total"):
                    name += "_total"
                if var:
                    decls[var.group(1)] = (f, name, Metric(name, kind, labels, os.path.relpath(f, repo)))
        for var, (_decl_file, name, metric) in decls.items():
            use = re.compile(r"\b" + re.escape(var) + r"\.(?:labels|inc|dec|set|observe|time)\(")
            if any(use.search(open(f, encoding="utf-8", errors="replace").read()) for f in served):
                out[name] = metric
    return out


def lookup(metric: str, emitted: dict[str, Metric]) -> Metric | None:
    if metric in emitted:
        return emitted[metric]
    for suffix in ("_bucket", "_count", "_sum"):
        if metric.endswith(suffix):
            base = emitted.get(metric[: -len(suffix)])
            if base and base.kind in ("histogram", "summary"):
                return base
    for prefix, source in EXPORTER_PREFIXES.items():
        if metric.startswith(prefix):
            return Metric(metric, "external", None, source)
    return None


def selectors(expr: str) -> list[tuple[str, list[tuple[str, str, str]]]]:
    """(metric, [(label, op, value)]) for every vector selector in expr."""
    out = []
    i, n = 0, len(expr)
    last_metric_idx = None
    while i < n:
        c = expr[i]
        if c.isspace():
            i += 1
        elif c in "\"'`":
            j = i + 1
            while j < n and expr[j] != c:
                j += 2 if expr[j] == "\\" else 1
            i = j + 1
        elif c == "[":
            i = expr.index("]", i) + 1
        elif c == "{":
            j = expr.index("}", i)
            matchers = MATCHER.findall(expr[i + 1 : j])
            if last_metric_idx is not None:
                out[last_metric_idx][1].extend(matchers)
            i = j + 1
        elif c.isalpha() or c in "_:":
            j = i
            while j < n and (expr[j].isalnum() or expr[j] in "_:"):
                j += 1
            word = expr[i:j]
            k = j
            while k < n and expr[k].isspace():
                k += 1
            nxt = expr[k] if k < n else ""
            last_metric_idx = None
            if word in LABEL_LIST_KEYWORDS and nxt == "(":
                i = expr.index(")", k) + 1
                continue
            if nxt == "(" or word in PROMQL_FUNCS_AND_KEYWORDS:
                i = j
                continue
            out.append((word, []))
            last_metric_idx = len(out) - 1
            i = j
        elif c.isdigit() or c == ".":
            j = i
            while j < n and (expr[j].isalnum() or expr[j] in "._"):
                j += 1
            i = j
        else:
            last_metric_idx = None
            i += 1
    return out


def rule_files(repo: str) -> list[str]:
    return sorted(glob.glob(os.path.join(repo, "deploy", "monitoring", "alerts", "*.yml")))


def dashboard_exprs(path: str) -> list[str]:
    exprs: list[str] = []

    def walk(node):
        if isinstance(node, dict):
            for k, v in node.items():
                if k == "expr" and isinstance(v, str):
                    exprs.append(v)
                elif k == "query" and isinstance(v, str) and v.startswith("label_values("):
                    exprs.append(v[len("label_values("):].split(",")[0].rstrip(")"))
                else:
                    walk(v)
        elif isinstance(node, list):
            for v in node:
                walk(v)

    walk(json.load(open(path)))
    return exprs


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
    repo = os.path.abspath(ap.parse_args().repo)

    emitted: dict[str, Metric] = {}
    emitted.update(collect_go(repo))
    emitted.update(collect_python(repo))
    for name, (labels, source) in EXPORTERS.items():
        kind = "histogram" if name.endswith("_ms") else "external"
        emitted.setdefault(name, Metric(name, kind, labels, source))

    errors: list[str] = list(UNRESOLVED)
    checked: list[tuple[str, str]] = []  # (where, expr)

    for f in rule_files(repo):
        rel = os.path.relpath(f, repo)
        doc = yaml.safe_load(open(f)) or {}
        for g in doc.get("groups", []):
            for r in g.get("rules", []):
                if "record" in r:
                    emitted[r["record"]] = Metric(r["record"], "recorded", None, rel)
        for g in doc.get("groups", []):
            for r in g.get("rules", []):
                name = r.get("alert") or r.get("record")
                checked.append((f"{rel}:{name}", str(r.get("expr", ""))))
                if "alert" not in r:
                    continue
                labels = r.get("labels") or {}
                if labels.get("severity") not in SEVERITIES:
                    errors.append(f"{rel}:{name}: severity {labels.get('severity')!r} is not one of {SEVERITIES}")
                if not labels.get("service"):
                    errors.append(f"{rel}:{name}: no service label")
                if not (r.get("annotations") or {}).get("summary"):
                    errors.append(f"{rel}:{name}: no summary annotation")

    for f in sorted(glob.glob(os.path.join(repo, "deploy", "monitoring", "dashboards", "*.json"))):
        rel = os.path.relpath(f, repo)
        for e in dashboard_exprs(f):
            checked.append((rel, e))

    k8s_only = {"kube_", "node_"}
    for where, expr in checked:
        for metric, matchers in selectors(expr):
            m = lookup(metric, emitted)
            if m is None:
                errors.append(f"{where}: metric {metric} is not emitted by any service, recording rule or listed exporter")
                continue
            if any(metric.startswith(p) for p in k8s_only) and "kubernetes.yml" not in where:
                errors.append(f"{where}: {metric} exists only on Kubernetes; put the rule in alerts/kubernetes.yml")
            if m.labels is None:
                continue
            if m.source.startswith(("services/", "pkg/", "cmd/")):
                for clash in sorted(m.labels & COLLIDING_LABELS):
                    for lst in AGG_LIST.findall(expr):
                        names = {x.strip() for x in lst.split(",")}
                        if clash in names and f"exported_{clash}" not in names:
                            errors.append(
                                f"{where}: {metric} carries its own {clash!r} label, which Kubernetes "
                                f"scrapes as exported_{clash}; aggregate by ({clash}, exported_{clash})")
            allowed = m.labels | TARGET_LABELS | {f"exported_{x}" for x in COLLIDING_LABELS}
            for label, op, val in matchers:
                if label not in allowed:
                    errors.append(f"{where}: {metric} has no label {label!r} (has: {sorted(m.labels)})")
                    continue
                known = m.values.get(label)
                if not known or val == "":
                    continue
                if op in ("=", "!="):
                    if val not in known:
                        errors.append(f"{where}: {metric}{{{label}{op}{val!r}}}: the code only produces {label} in {sorted(known)}")
                else:
                    pattern = val.replace("\\\\", "\\")
                    try:
                        hit = any(re.fullmatch(pattern, k) for k in known)
                    except re.error as exc:
                        errors.append(f"{where}: {metric}: bad regex {val!r}: {exc}")
                        continue
                    if not hit:
                        errors.append(f"{where}: {metric}{{{label}{op}{val!r}}} matches none of {sorted(known)}")

    am_path = os.path.join(repo, "deploy", "monitoring", "alertmanager.yml")
    am_text = open(am_path).read()
    am = yaml.safe_load(am_text)
    # O-1: Alertmanager does not expand env vars; credentials come from files.
    if "${" in am_text:
        errors.append("deploy/monitoring/alertmanager.yml: ${...} is sent verbatim; use *_file")
    inline_secret_keys = {"service_key", "routing_key", "api_url", "auth_password", "url"}
    for rcv in am.get("receivers", []) or []:
        for kind, cfgs in rcv.items():
            if kind == "name":
                continue
            for cfg in cfgs or []:
                for key in inline_secret_keys & set(cfg):
                    errors.append(f"deploy/monitoring/alertmanager.yml: receiver {rcv['name']} sets {key} inline; use {key}_file")
    routed = set()

    def walk_routes(route):
        for sub in route.get("routes", []) or []:
            for mt in sub.get("matchers", []) or []:
                mm = re.match(r'\s*severity\s*=\s*"?([a-z0-9]+)"?\s*$', mt)
                if mm:
                    routed.add(mm.group(1))
            walk_routes(sub)

    walk_routes(am.get("route", {}))
    for sev in SEVERITIES:
        if sev not in routed:
            errors.append(f"deploy/monitoring/alertmanager.yml: no route for severity {sev}")

    errors = list(dict.fromkeys(errors))  # keep order, drop repeats
    if errors:
        print(f"check-metrics: {len(errors)} problem(s)")
        for e in errors:
            print("  " + e)
        return 1
    print(f"check-metrics: OK — {len(checked)} expressions, {len(emitted)} known metrics")
    return 0


if __name__ == "__main__":
    sys.exit(main())
