#!/usr/bin/env bash
# §3.2 / A2 — `wait-for-healthy` wrapper around the existing
# wait-for-health.sh (kept because it is referenced from CI and
# Makefile). Takes the same TIMEOUT env var; exits 0 on all-healthy,
# 1 on timeout.
set -euo pipefail
exec "$(dirname "$0")/wait-for-health.sh" "$@"
