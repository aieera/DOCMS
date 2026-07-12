"""Lint: NATS subject literals are forbidden in app/tasks/ — import from
app.events.subjects instead.

Why: inline subject strings are how dms.notification.send.v1,
dms.translation.completed.v1, and dms.training_example.collected.v1
shipped without JetStream stream bindings and (pre-fix) could wedge the
shared outbox drain (STATE 2026-07-03 §C). The manifest is the single
source of truth; the Go build gate (pkg/events/coverage_python_test.go)
parses it and fails when a manifest subject is unbound or missing from
PublishedSubjects. This lint closes the remaining hole: a task defining
its own literal would bypass the manifest.

Pure-stdlib AST checks — no app imports, so it runs under bare pytest.
Docstrings are exempt (they legitimately mention subjects in prose).
"""
from __future__ import annotations

import ast
import re
from pathlib import Path

SERVICE_ROOT = Path(__file__).resolve().parents[1]
TASKS_DIR = SERVICE_ROOT / "app" / "tasks"
MANIFEST = SERVICE_ROOT / "app" / "events" / "subjects.py"

SUBJECT_RE = re.compile(r"^dms\.[a-z0-9_]+(?:\.[a-z0-9_]+)*\.v\d+$")


def _docstring_const_ids(tree: ast.AST) -> set[int]:
    """ids of the Constant nodes that are module/class/function docstrings."""
    out: set[int] = set()
    for node in ast.walk(tree):
        if isinstance(node, (ast.Module, ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)):
            body = getattr(node, "body", [])
            if (
                body
                and isinstance(body[0], ast.Expr)
                and isinstance(body[0].value, ast.Constant)
                and isinstance(body[0].value.value, str)
            ):
                out.add(id(body[0].value))
    return out


def test_no_inline_subject_literals_in_tasks():
    offenders: list[str] = []
    files = sorted(TASKS_DIR.glob("*.py"))
    assert files, f"no task files found under {TASKS_DIR} — lint is miswired"
    for py in files:
        tree = ast.parse(py.read_text(), filename=str(py))
        doc_ids = _docstring_const_ids(tree)
        for node in ast.walk(tree):
            if (
                isinstance(node, ast.Constant)
                and isinstance(node.value, str)
                and id(node) not in doc_ids
                and SUBJECT_RE.match(node.value)
            ):
                offenders.append(f"{py.name}:{node.lineno}: {node.value!r}")
    assert not offenders, (
        "inline NATS subject literal(s) in app/tasks/ — declare the subject in "
        "app/events/subjects.py and import it (the Go gate in "
        "pkg/events/coverage_python_test.go then enforces its stream binding):\n  "
        + "\n  ".join(offenders)
    )


def test_manifest_is_wellformed():
    tree = ast.parse(MANIFEST.read_text(), filename=str(MANIFEST))
    consts: dict[str, str] = {}
    for node in tree.body:
        if isinstance(node, ast.Assign) and isinstance(node.value, ast.Constant):
            value = node.value.value
            if not isinstance(value, str):
                continue
            (target,) = node.targets
            assert isinstance(target, ast.Name), f"unexpected manifest target at line {node.lineno}"
            name = target.id
            assert name.isupper(), f"{name}: manifest constants must be UPPER_SNAKE"
            assert SUBJECT_RE.match(value), f"{name} = {value!r}: not a dms.*.vN subject"
            assert value not in consts.values(), f"{name}: duplicate subject value {value!r}"
            consts[name] = value
    assert len(consts) >= 20, (
        f"manifest parsed only {len(consts)} subjects — parser or manifest broken"
    )
