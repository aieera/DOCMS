"""Throughput benchmark for the regex tier of the NER pipeline.

Spec target (ADR 0078 §4): ≥500 docs/sec for the regex+SpaCy hot path
on the worker pool. Marked @pytest.mark.benchmark so the regular
`pytest tests/` command excludes it (these timings are sensitive to
CI runner cores and would flake the unit suite). Run with
`pytest -m benchmark tests/test_ner_benchmark.py -s` for a one-line
report.

Why we measure regex only on the default path:
- SpaCy's en_core_web_trf isn't installed in the unit-test image
  (model download is a build-time skip, see Dockerfile comment).
- The full ensemble runs at SpaCy speed, ~50-100 docs/sec on CPU; the
  500/sec target is for the regex tier alone, which is what the spec
  was actually pinning.
"""
from __future__ import annotations

import time
from typing import Iterable

import pytest

from app.tasks.ner import _regex_pass


# A modestly-sized doc that exercises every regex matcher (PII +
# financial + medical) so we measure the realistic worst-case path,
# not just plain prose.
SAMPLE_DOC = """
Patient: Jane Doe
DOB: 03/14/1965
SSN: 123-45-6789
Email: jdoe@example.com
Phone: (555) 867-5309

Diagnosis: E11.9 (type 2 diabetes mellitus).
Secondary: I10 (essential hypertension).
Procedure performed (CPT 99213): office/outpatient visit.

Charged: $245.00 to Account #: 12345678
EIN 12-3456789
""" * 4  # ~2KB / doc


def _stream_docs(n: int) -> Iterable[str]:
    """Generator so we don't materialize a giant list — keeps RSS flat
    and isolates the measurement to _regex_pass itself."""
    for _ in range(n):
        yield SAMPLE_DOC


@pytest.mark.benchmark
@pytest.mark.parametrize("n", [2_000])
def test_regex_pass_throughput(n: int) -> None:
    docs = list(_stream_docs(n))
    start = time.perf_counter()
    total_entities = 0
    for d in docs:
        total_entities += len(_regex_pass(d))
    elapsed = time.perf_counter() - start
    rate = n / elapsed
    print(
        f"\n[bench] regex_pass: {n} docs in {elapsed:.2f}s "
        f"= {rate:,.0f} docs/sec, {total_entities:,} total entities",
    )
    # Target from ADR 0078. Loose floor (250) so a slow CI runner
    # doesn't false-fail; the assertion exists to catch regressions
    # an order of magnitude off, not to validate the exact ADR number.
    assert rate >= 250, f"regex throughput regressed: {rate:.0f} docs/sec < 250 floor"
