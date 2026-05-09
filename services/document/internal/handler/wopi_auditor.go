// ADR 0065 — concrete WOPIAuditor that emits to the document
// service's existing outbox so the audit service consumes the same
// dms.coauth.session.{started,ended}.v1 subjects every other audit
// row uses. Survives crashes via the outbox publisher.
//
// Wired in main.go after the WOPIHandler is constructed:
//
//	h := NewWOPIHandler(rdb, log)
//	h.Auditor = NewOutboxWOPIAuditor(pool, outboxRepo, log)
package handler

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/database"
)

// outboxWOPIAuditor implements WOPIAuditor by inserting an outbox
// row for each session_started and session_ended event.
type outboxWOPIAuditor struct {
	pool   *pgxpool.Pool
	outbox *database.OutboxRepository
	log    zerolog.Logger
}

// NewOutboxWOPIAuditor constructs the concrete auditor. Pass the
// same outbox repo the rest of the document service uses so events
// land in one publisher.
func NewOutboxWOPIAuditor(pool *pgxpool.Pool, outbox *database.OutboxRepository, log zerolog.Logger) WOPIAuditor {
	return &outboxWOPIAuditor{pool: pool, outbox: outbox, log: log}
}

// SessionStarted writes dms.coauth.session.started.v1 to the outbox.
// Background context with a 5s deadline so a stuck Postgres can't
// hold a WOPI request goroutine. Errors are logged — audit is
// best-effort here; the WOPI request must still succeed.
func (a *outboxWOPIAuditor) SessionStarted(c *WOPIClaims) {
	a.emit(c, "dms.coauth.session.started.v1", 0)
}

// SessionEnded writes dms.coauth.session.ended.v1 with the duration.
func (a *outboxWOPIAuditor) SessionEnded(c *WOPIClaims, durationSeconds int64) {
	a.emit(c, "dms.coauth.session.ended.v1", durationSeconds)
}

func (a *outboxWOPIAuditor) emit(c *WOPIClaims, subject string, durationSeconds int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payload := map[string]any{
		"specversion": "1.0",
		"type":        subject,
		"source":      "/vaultdms/document/wopi",
		"data": map[string]any{
			"tenant_id":  c.TenantID.String(),
			"user_id":    c.UserID.String(),
			"version_id": c.FileID.String(),
			"can_write":  c.CanWrite,
			"at":         time.Now().UTC().Format(time.RFC3339),
		},
	}
	if durationSeconds > 0 {
		payload["data"].(map[string]any)["duration_seconds"] = durationSeconds
	}
	body, err := json.Marshal(payload)
	if err != nil {
		a.log.Warn().Err(err).Str("subject", subject).Msg("wopi audit: marshal failed")
		return
	}
	// Aggregate id = the version. Ties events together when an
	// auditor queries "every session on this version".
	evt := database.NewOutboxEvent(c.TenantID, subject, "document_version", c.FileID, body)
	if err := database.WithTenantTx(ctx, a.pool, c.TenantID, func(tx pgx.Tx) error {
		return a.outbox.Insert(ctx, tx, evt)
	}); err != nil {
		a.log.Warn().Err(err).Str("subject", subject).Msg("wopi audit: outbox insert failed")
	}
}

// Compile-time interface assertion.
var _ WOPIAuditor = (*outboxWOPIAuditor)(nil)

// Silence unused imports when the file is built alone.
var _ = uuid.Nil
