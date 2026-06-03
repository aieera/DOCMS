#!/usr/bin/env bash
# Preflight: verify every NATS subject our services subscribe to or
# publish is covered by a provisioned JetStream stream. Exits non-zero
# on the first gap, printing which service needs the uncovered subject.
#
# Why: three separate dev-stack incidents (audit/connector subscribing
# `dms.>`, outbox publishing `dms.auth.*` into a topology with no
# matching stream) all presented as confusing runtime errors after the
# services were already running. A 10-second check up front would have
# caught each of them before any service started.
#
# Usage:
#   source scripts/preflight.sh
#   preflight            # prints a line per subject, exits 1 on any miss
#
# Requirements: dms-admin binary at repo root, NATS reachable at
# $SEDOC_NATS_URL (or nats://localhost:4222 by default).

set -euo pipefail

# REQUIRED_SUBJECTS is "<service>:<subject>" — the subject is what the
# service subscribes to or publishes; the service tag is for error
# reporting. Keep this list in sync with:
#   * services/*/internal/service/*.go  (subscribes)
#   * pkg/events/publisher.go            (produces via outbox)
REQUIRED_SUBJECTS=(
  # audit — needs everything (see services/audit/internal/service/service.go)
  "audit:dms.document.>"
  "audit:dms.version.>"
  "audit:dms.workspace.>"
  "audit:dms.user.>"
  "audit:dms.session.>"
  "audit:dms.apikey.>"
  "audit:dms.auth.>"
  "audit:dms.policy.>"
  "audit:dms.permission.>"
  "audit:dms.billing.>"
  "audit:dms.subscription.>"
  "audit:dms.usage.>"
  "audit:dms.audit.>"
  "audit:dms.search.>"
  "audit:dms.workflow.>"
  "audit:dms.task.>"
  "audit:dms.ocr.>"
  "audit:dms.classify.>"
  "audit:dms.embed.>"
  "audit:dms.ner.>"
  "audit:dms.notify.>"
  "audit:dms.sharelink.>"
  "audit:dms.folder.>"
  "audit:dms.intelligence.>"
  "audit:dms.rotation.>"
  # connector — same coverage (fanout to webhooks)
  "connector:dms.document.>"
  "connector:dms.user.>"
  "connector:dms.auth.>"
  "connector:dms.policy.>"
  "connector:dms.billing.>"
  "connector:dms.audit.>"
  "connector:dms.workflow.>"
  "connector:dms.notify.>"
  # search indexer (see services/search/internal/service/indexer.go)
  "search:dms.document.created.v1"
  "search:dms.document.updated.v1"
  "search:dms.document.deleted.v1"
  "search:dms.version.ocr_completed.v1"
  "search:dms.permission.changed.v1"
  "search:dms.version.classified.v1"
  "search:dms.version.entities_detected.v1"
  # outbox publishers (event_type names seen in the outbox table — any
  # service running this codebase may publish these from pkg/database
  # outbox_publisher on shared auth/session/api_key flows)
  "outbox:dms.auth.login_success.v1"
  "outbox:dms.auth.api_key_issued.v1"
  "outbox:dms.auth.api_key_revoked.v1"
)

# preflight returns 0 iff every REQUIRED_SUBJECT is covered by at least
# one stream's subject filter. Uses `dms-admin nats check`, which speaks
# real NATS subject semantics via the nats.go client — far more reliable
# than bash string matching.
preflight() {
  local repo_root
  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  local admin="$repo_root/dms-admin.exe"
  if [ ! -x "$admin" ] && [ -x "$repo_root/dms-admin" ]; then
    admin="$repo_root/dms-admin"
  fi
  if [ ! -x "$admin" ]; then
    echo "preflight: dms-admin binary not found; run: (cd $repo_root && go build -o dms-admin.exe ./cmd/dms-admin)" >&2
    return 2
  fi

  # Collect subjects once; retain service-to-subject for error reporting.
  local -a subjects=()
  local pair svc subj
  for pair in "${REQUIRED_SUBJECTS[@]}"; do
    subjects+=("${pair#*:}")
  done

  echo "preflight: checking ${#subjects[@]} subjects against JetStream topology..."
  local tmp
  tmp="$(mktemp)"
  trap 'rm -f "$tmp"' RETURN
  if ! "$admin" nats check "${subjects[@]}" > "$tmp" 2>&1; then
    echo "" >&2
    echo "✗ preflight FAILED — uncovered subjects:" >&2
    grep '^MISS ' "$tmp" | while read -r _ s _; do
      for pair in "${REQUIRED_SUBJECTS[@]}"; do
        if [ "${pair#*:}" = "$s" ]; then
          echo "  ✗ $s  required by: ${pair%%:*}" >&2
        fi
      done
    done
    echo "" >&2
    echo "Fix: either add the subject to a stream in pkg/events/publisher.go" >&2
    echo "then run  ./dms-admin.exe nats bootstrap  to apply the update," >&2
    echo "or change the subscriber/publisher to a subject that exists." >&2
    return 1
  fi
  echo "preflight: OK — all subjects covered"
  return 0
}
