#!/usr/bin/env bash
# Wave 6 Prompt 6.5 — forbid direct NATS publishes from services.
#
# Rationale (final.md § 5.5): a publish that runs OUTSIDE the DB tx
# that produced the state change is non-atomic. On a crash between
# the commit and the publish, the event is lost forever — no
# reconciliation path exists. The outbox pattern (DB row + publisher
# worker) closes that window.
#
# Allow-list:
#   - pkg/database/outbox_publisher.go — the outbox publisher itself;
#     drains the outbox table and is the ONLY legitimate NATS publish
#     origin.
#   - pkg/events/publisher.go — generic CloudEvents publisher library.
#     Not called by any service today; kept in place for emergency
#     / diagnostic tooling. If it starts appearing in services, this
#     rule's first line of defence is a human review via the CI fail.
#   - cmd/dms-admin — admin CLI may publish diagnostic messages.
#   - *_test.go — test fixtures.

set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"

violations=0
while IFS= read -r file; do
    case "$file" in
        *_test.go)                                  continue ;;
        */pkg/database/outbox_publisher.go)         continue ;;
        */pkg/events/publisher.go)                  continue ;;
        */cmd/dms-admin/*)                          continue ;;
    esac
    # Match js.Publish(, js.PublishMsg(, js.PublishAsync(.
    hits=$(grep -nE 'js\.Publish(Msg|Async)?\(' "$file" 2>/dev/null || true)
    if [ -n "$hits" ]; then
        echo "[FAIL] $file: direct JetStream publish — use pkg/database.OutboxRepository.Insert within a tx:"
        echo "$hits" | sed 's/^/    /'
        violations=$((violations + 1))
    fi
done < <(find "$REPO/services" -type f -name '*.go' 2>/dev/null)

if [ "$violations" -gt 0 ]; then
    cat <<'EOF'

Fix pattern:

    # Wrong (direct publish, non-atomic with DB commit):
    err := js.Publish(subject, payload)

    # Right (outbox in the same tx as the state change):
    evt, err := model.NewOutboxEvent(tenantID, eventType, aggregateType, aggregateID, payload)
    if err != nil { return err }
    return s.repos.Outbox.Insert(ctx, tx, evt)

See ADR 0021 + pkg/database/outbox_publisher.go for the publisher
drain loop.
EOF
    exit 1
fi

echo "ok: no direct NATS publishes in services (outbox is the only write path)"
exit 0
