#!/usr/bin/env python3
"""Tests for check-metrics.py against small fixture repos.

Run: python3 deploy/monitoring/test_check_metrics.py
"""
from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import textwrap
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "check-metrics.py")

GO_METRICS = '''\
package svc

import "github.com/prometheus/client_golang/prometheus"

var jobsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "sedoc",
	Subsystem: "jobs",
	Name:      "runs_total",
	Help:      "Job runs by outcome.",
}, []string{"result", "kind"})

func record(ok bool, kind string) {
	if ok {
		jobsTotal.WithLabelValues("ok", kind).Inc()
		return
	}
	jobsTotal.WithLabelValues("error", kind).Inc()
}
'''

GO_MAIN = '''\
package main

import _ "github.com/aieera/sedoc/services/x/internal/svc"

func main() {}
'''

ALERTMANAGER = '''\
route:
  receiver: r
  routes:
    - {matchers: [severity="p0"], receiver: r}
    - {matchers: [severity="p1"], receiver: r}
    - {matchers: [severity="p2"], receiver: r}
    - {matchers: [severity="p3"], receiver: r}
receivers:
  - name: r
'''


def rule(expr: str) -> str:
    return textwrap.dedent(f'''\
        groups:
          - name: g
            rules:
              - alert: A
                expr: '{expr}'
                labels: {{severity: p2, service: x}}
                annotations: {{summary: s}}
        ''')


def run(expr: str) -> subprocess.CompletedProcess:
    with tempfile.TemporaryDirectory() as repo:
        files = {
            "services/x/internal/svc/metrics.go": GO_METRICS,
            "services/x/cmd/server/main.go": GO_MAIN,
            "deploy/monitoring/alerts/r.yml": rule(expr),
            "deploy/monitoring/alertmanager.yml": ALERTMANAGER,
        }
        os.makedirs(os.path.join(repo, "deploy/monitoring/dashboards"))
        for rel, content in files.items():
            path = os.path.join(repo, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w") as fh:
                fh.write(content)
        return subprocess.run([sys.executable, SCRIPT, "--repo", repo],
                              capture_output=True, text=True)


class CheckMetricsTest(unittest.TestCase):
    def test_namespace_and_subsystem_compose_the_name(self):
        r = run('sum(rate(sedoc_jobs_runs_total{result="error"}[5m])) > 0')
        self.assertEqual(r.returncode, 0, r.stdout)

    def test_bare_name_without_namespace_is_not_emitted(self):
        r = run('sum(rate(runs_total[5m])) > 0')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("runs_total is not emitted", r.stdout)

    def test_unknown_label_value_is_rejected(self):
        r = run('sum(rate(sedoc_jobs_runs_total{result="failed"}[5m])) > 0')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("'failed'", r.stdout)
        self.assertIn("result", r.stdout)

    def test_regex_matching_no_known_value_is_rejected(self):
        r = run('sum(rate(sedoc_jobs_runs_total{result=~"fail.*"}[5m])) > 0')
        self.assertNotEqual(r.returncode, 0)

    def test_regex_matching_a_known_value_passes(self):
        r = run('sum(rate(sedoc_jobs_runs_total{result=~"ok|error"}[5m])) > 0')
        self.assertEqual(r.returncode, 0, r.stdout)

    def test_label_with_non_literal_values_is_not_constrained(self):
        r = run('sum(rate(sedoc_jobs_runs_total{kind="anything"}[5m])) > 0')
        self.assertEqual(r.returncode, 0, r.stdout)


if __name__ == "__main__":
    unittest.main()
