// Package activities — GDPR DSR activities (Wave 8 Prompt 8.3).
//
// These activities are the units of work stitched together by
// ExportWorkflow / EraseWorkflow / AnonymizeWorkflow. All of them
// are idempotent on re-invocation (Temporal may replay any activity)
// and tenant-scoped via WithTenantTx.
//
// Scope note: the real PII-scrubbing for cross-service tables
// (auth.users, qdrant payloads, notification_preferences) requires
// a registry of opt-in tables we don't have yet. See ADR 0024 §
// Consequences and the out-of-scope ledger. Wave 8.3 covers the
// document service's own rows + the privacy ledger; other services
// register activities in Wave 11.
package activities

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/aieera/sedoc/pkg/database"
)

// ResolveSubject returns the user UUID for a given email inside the
// tenant. Returns uuid.Nil + no error if the subject doesn't exist —
// DSR semantics say "absent subject" is a completed-with-no-action
// outcome, not a failure.
func (a *Activities) ResolveSubject(ctx context.Context, tenantID, email string) (string, error) {
	var id string
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id::text FROM users
			 WHERE tenant_id = $1 AND lower(email) = lower($2) AND deleted_at IS NULL
			 LIMIT 1`,
			tenantID, email,
		).Scan(&id)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("resolve subject: %w", err)
	}
	return id, nil
}

// SubjectHasHeldDocuments reports whether the subject authored or is
// attached to any document currently under an active legal hold.
// Used as the erase / anonymize short-circuit per ADR 0024 §2.
func (a *Activities) SubjectHasHeldDocuments(ctx context.Context, tenantID, subjectID string) (bool, error) {
	var exists bool
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1
			      FROM legal_hold_documents lhd
			      JOIN legal_holds lh
			        ON lh.tenant_id = lhd.tenant_id AND lh.id = lhd.hold_id
			      JOIN documents d
			        ON d.tenant_id = lhd.tenant_id AND d.id = lhd.document_id
			     WHERE lhd.tenant_id = $1
			       AND lh.is_active = true
			       AND d.created_by = $2
			)`, tenantID, subjectID,
		).Scan(&exists)
	})
	return exists, err
}

// DSRSubjectSummary is the count-shaped response used by both
// CollectSubjectData (export's manifest header) and the result_summary
// JSONB written to privacy_dsr_requests on completion.
type DSRSubjectSummary struct {
	SubjectID       string `json:"subject_id"`
	DocumentsOwned  int    `json:"documents_owned"`
	TasksAssigned   int    `json:"tasks_assigned"`
	AuditEvents     int    `json:"audit_events"`
	HeldDocuments   int    `json:"held_documents"`
}

// CollectSubjectData walks the document service's tables counting
// rows attributable to the subject. Returning counts rather than row
// sets keeps the activity payload small; the ZIP packaging step
// re-queries with pagination from the workflow.
func (a *Activities) CollectSubjectData(ctx context.Context, tenantID, subjectID string) (*DSRSubjectSummary, error) {
	out := &DSRSubjectSummary{SubjectID: subjectID}
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM documents WHERE tenant_id = $1 AND created_by = $2`,
			tenantID, subjectID,
		).Scan(&out.DocumentsOwned); err != nil {
			return fmt.Errorf("count documents: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM workflow_tasks WHERE tenant_id = $1 AND assignee_id = $2`,
			tenantID, subjectID,
		).Scan(&out.TasksAssigned); err != nil {
			return fmt.Errorf("count tasks: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2`,
			tenantID, subjectID,
		).Scan(&out.AuditEvents); err != nil {
			return fmt.Errorf("count audit: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM documents d
			 WHERE d.tenant_id = $1 AND d.created_by = $2
			   AND EXISTS (
			       SELECT 1 FROM legal_hold_documents lhd
			         JOIN legal_holds lh ON lh.id = lhd.hold_id AND lh.tenant_id = lhd.tenant_id
			        WHERE lhd.tenant_id = d.tenant_id AND lhd.document_id = d.id AND lh.is_active = true
			   )`, tenantID, subjectID,
		).Scan(&out.HeldDocuments); err != nil {
			return fmt.Errorf("count held: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OverwriteSubjectPII redacts PII columns on rows attributable to the
// subject. `mode` selects between:
//
//   - "erase"     — replaces values with `erased-<uuid>`.
//   - "anonymize" — replaces values with HMAC-SHA256(value, tenant_salt)
//     hex digest. Aggregate analytics still group correctly by hash.
//
// Documents authored by the subject are NOT disposed here — the
// workflow calls RetentionTransition separately so the lifecycle
// audit event is distinct. This activity only touches PII columns.
//
// Wave 11.6: scope expanded to the full set of user-keyed tables
// in the document DB. On `erase` mode every user-identifying row is
// deleted or redacted; on `anonymize` mode aggregate-analytics
// tables (group/workspace membership) are preserved so cohort
// analytics still group by the hashed subject id.
//
// Tables touched:
//
//	users                      redact display_name + email (both modes)
//	audit_events               scrub user_agent + metadata (both modes)
//	sessions                   revoke all (erase only)
//	api_keys                   revoke all (erase only)
//	notifications              delete all (erase only)
//	notification_preferences   delete all (erase only)
//	device_tokens              delete all (erase only)
//	conversation_history       delete all (erase only; RAG history is PII)
//	group_members              delete all (erase only)
//	workspace_members          delete all (erase only)
//
// Anonymize keeps memberships + notifications because those are
// aggregate-analytic signals; only identifiers are hashed.
//
// A single tenant-scoped transaction wraps the whole set so either
// every scrub lands or none does.
func (a *Activities) OverwriteSubjectPII(ctx context.Context, tenantID, subjectID, mode, tenantSalt string) (int, error) {
	if mode != "erase" && mode != "anonymize" {
		return 0, fmt.Errorf("invalid mode: %s", mode)
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return 0, fmt.Errorf("tenant_id: %w", err)
	}

	redactedEmail := redactValue("email", subjectID, mode, tenantSalt)
	redactedName := redactValue("name", subjectID, mode, tenantSalt)
	now := time.Now().UTC()

	var rowsAffected int64
	err = database.WithTenantTx(ctx, a.Pool, tenantUUID, func(tx pgx.Tx) error {
		// ---- users -------------------------------------------------------
		tag, err := tx.Exec(ctx, `
			UPDATE users
			   SET display_name = $1, email = $2, updated_at = $3
			 WHERE tenant_id = $4 AND id = $5`,
			redactedName, redactedEmail, now, tenantID, subjectID,
		)
		if err != nil {
			return fmt.Errorf("redact users: %w", err)
		}
		rowsAffected += tag.RowsAffected()

		// ---- audit_events (both modes) -----------------------------------
		// Keep actor_id for the completeness guarantee; scrub free-text.
		if _, err := tx.Exec(ctx, `
			UPDATE audit_events
			   SET user_agent = 'redacted',
			       metadata   = jsonb_build_object('redacted', true, 'mode', $1)
			 WHERE tenant_id = $2 AND actor_id = $3`,
			mode, tenantID, subjectID,
		); err != nil {
			return fmt.Errorf("redact audit: %w", err)
		}

		if mode != "erase" {
			return nil
		}

		// ---- erase-only: delete / revoke across the long tail -----------

		// Sessions — revoke so an in-flight token stops working.
		if tag, err := tx.Exec(ctx, `
			UPDATE sessions SET revoked_at = $1
			 WHERE tenant_id = $2 AND user_id = $3 AND revoked_at IS NULL`,
			now, tenantID, subjectID,
		); err == nil {
			rowsAffected += tag.RowsAffected()
		} else {
			return fmt.Errorf("revoke sessions: %w", err)
		}

		// API keys — same.
		if tag, err := tx.Exec(ctx, `
			UPDATE api_keys SET revoked_at = $1
			 WHERE tenant_id = $2 AND user_id = $3 AND revoked_at IS NULL`,
			now, tenantID, subjectID,
		); err == nil {
			rowsAffected += tag.RowsAffected()
		} else {
			return fmt.Errorf("revoke api keys: %w", err)
		}

		// Notifications + preferences + device tokens — delete outright.
		// No retention obligation on in-app messages.
		for _, sql := range []string{
			`DELETE FROM notifications           WHERE tenant_id = $1 AND user_id = $2`,
			`DELETE FROM notification_preferences WHERE tenant_id = $1 AND user_id = $2`,
			`DELETE FROM device_tokens            WHERE tenant_id = $1 AND user_id = $2`,
		} {
			if tag, err := tx.Exec(ctx, sql, tenantID, subjectID); err == nil {
				rowsAffected += tag.RowsAffected()
			} else {
				return fmt.Errorf("delete user-scoped rows: %w", err)
			}
		}

		// Conversation history (RAG chat) — PII-heavy free text.
		if tag, err := tx.Exec(ctx,
			`DELETE FROM conversation_history WHERE tenant_id = $1 AND user_id = $2`,
			tenantID, subjectID,
		); err == nil {
			rowsAffected += tag.RowsAffected()
		} else {
			return fmt.Errorf("delete conversations: %w", err)
		}

		// Group + workspace memberships — subject no longer belongs
		// anywhere after erase.
		for _, sql := range []string{
			`DELETE FROM group_members     WHERE tenant_id = $1 AND user_id = $2`,
			`DELETE FROM workspace_members WHERE tenant_id = $1 AND user_id = $2`,
		} {
			if tag, err := tx.Exec(ctx, sql, tenantID, subjectID); err == nil {
				rowsAffected += tag.RowsAffected()
			} else {
				return fmt.Errorf("delete memberships: %w", err)
			}
		}

		return nil
	})
	return int(rowsAffected), err
}

// WritePrivacyLedger appends one row to privacy_ledger. Returns the
// ledger id so the workflow can reference it in the emitted
// dms.dsr.* event.
func (a *Activities) WritePrivacyLedger(ctx context.Context, tenantID, requestID, subjectEmail, action, outcome string, details map[string]any) (string, error) {
	payload, _ := json.Marshal(details)
	id, _ := uuid.NewV7()
	// privacy_ledger is intentionally NOT RLS-wrapped (ADR 0024 §5);
	// we still run through runTenant for consistency + to set the
	// tenant GUC for logging/audit interceptors.
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO privacy_ledger (id, tenant_id, request_id, subject_email, action, outcome, details, created_at)
			VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8)`,
			id, tenantID, requestID, subjectEmail, action, outcome, payload, time.Now().UTC(),
		)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("write ledger: %w", err)
	}
	return id.String(), nil
}

// UpdateDSRRequest sets status / workflow_run_id / summary / blocked_reason
// on the privacy_dsr_requests row. Called by the workflow at each
// state transition.
func (a *Activities) UpdateDSRRequest(ctx context.Context, tenantID, requestID, status, blockedReason string, summary map[string]any, exportURL string, expires *time.Time) error {
	summaryJSON, _ := json.Marshal(summary)
	completed := any(nil)
	if status == "completed" || status == "blocked" || status == "failed" {
		completed = time.Now().UTC()
	}
	return a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE privacy_dsr_requests
			   SET status = $1,
			       blocked_reason = NULLIF($2, ''),
			       result_summary = COALESCE(result_summary, '{}'::jsonb) || $3::jsonb,
			       export_url = NULLIF($4, ''),
			       export_url_expires_at = $5,
			       completed_at = $6
			 WHERE tenant_id = $7 AND id = $8`,
			status, blockedReason, summaryJSON, exportURL, expires, completed, tenantID, requestID,
		)
		return err
	})
}

// EmitDSREvent writes a dms.dsr.* outbox row. Workflow uses this for
// requested / completed / blocked emissions.
func (a *Activities) EmitDSREvent(ctx context.Context, tenantID, requestID, subjectEmail, subject string, data map[string]any) error {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	reqUUID := parseUUIDOrNew(requestID)

	data["tenant_id"] = tenantID
	data["request_id"] = requestID
	data["subject_email"] = subjectEmail

	payload, _ := json.Marshal(map[string]any{
		"specversion": "1.0",
		"type":        subject,
		"source":      "/vaultdms/workflow/dsr",
		"data":        data,
	})
	evt := database.NewOutboxEvent(tenantUUID, subject, "privacy_dsr", reqUUID, payload)
	return database.WithTenantTx(ctx, a.Pool, tenantUUID, func(tx pgx.Tx) error {
		return a.Outbox.Insert(ctx, tx, evt)
	})
}

// VerifyDSRToken is the Wave 11.4 real redemption path. The DSR
// verify endpoint stored SHA-256(token) in Redis at key
// `dsr:verify:{tenant}:{email}` with a 24h TTL; we re-hash the
// presented plaintext and constant-time compare.
//
// On success the key is DELed — tokens are single-use. Absent or
// mismatched tokens return an error that the workflow turns into
// a `failed` outcome + a ledger entry with outcome `verify-mismatch`.
//
// Activities never panic on a nil Redis client (some dev rigs run
// without it); we return a typed error so the workflow can log a
// clear message.
func (a *Activities) VerifyDSRToken(ctx context.Context, tenantID, email, token string) error {
	if a.Redis == nil {
		return fmt.Errorf("redis not configured; token verification unavailable")
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("token required")
	}
	key := fmt.Sprintf("dsr:verify:%s:%s", tenantID, strings.ToLower(strings.TrimSpace(email)))
	stored, err := a.Redis.Get(ctx, key).Result()
	if err == redis.Nil {
		return fmt.Errorf("token expired or never issued")
	}
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	h := sha256.Sum256([]byte(token))
	if !hmac.Equal([]byte(hex.EncodeToString(h[:])), []byte(stored)) {
		return fmt.Errorf("token mismatch")
	}
	// Single-use — delete so replay is not possible.
	_ = a.Redis.Del(ctx, key).Err()
	return nil
}

// redactValue implements the erase-vs-anonymize column rewrite.
//
// Erase:      "erased-<short-uuid>"
// Anonymize:  "anon-<hex digest prefix>"
//
// The digest uses HMAC-SHA256 keyed by the tenant salt so the same
// subject hashes consistently within a tenant (enabling aggregate
// analytics) but NOT across tenants.
func redactValue(field, subjectID, mode, tenantSalt string) string {
	if mode == "erase" {
		id, _ := uuid.NewV7()
		short := id.String()
		if len(short) > 8 {
			short = short[:8]
		}
		return "erased-" + short
	}
	// anonymize
	mac := hmac.New(sha256.New, []byte(tenantSalt))
	_, _ = mac.Write([]byte(field + ":" + subjectID))
	return "anon-" + hex.EncodeToString(mac.Sum(nil))[:16]
}
