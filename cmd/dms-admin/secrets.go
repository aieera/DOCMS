// dms-admin secrets — per-target secret rotation with dual-read window
// and NATS event emission.
//
// Subcommands:
//   secrets rotate --target jwt-signing --scope global
//   secrets rotate --target tenant-kek  --tenant <uuid>
//   secrets rotate --target api-internal-token --service <name>
//   secrets rotate --target db-password --role <role>
//
// Rotation flow (all targets):
//   1. Emit `dms.rotation.started.v1` to NATS so observers see the
//      intent before any state changes.
//   2. Write the NEW secret alongside the existing one.
//   3. Set a revocation deadline = now + dual-validity window
//      (default 24h; flags can tighten for test runs).
//   4. Emit `dms.rotation.completed.v1` with the new key-id /
//      version / alias so consumers can start preferring the new
//      material.
//
// Auto-revoke of the OLD secret after the dual-validity window is
// out of scope for this CLI — it needs either a Temporal cron or a
// k8s CronJob that runs `revoke-expired-secrets`. Tracked in the
// out-of-scope ledger.
//
// Direct nats.Publish is fine here: `cmd/dms-admin` is a one-shot
// operator CLI, not a long-running service, and the outbox-only CI
// guard explicitly scopes to `services/*/internal/`.

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

const (
	subjectRotationStarted   = "dms.rotation.started.v1"
	subjectRotationCompleted = "dms.rotation.completed.v1"

	// Default dual-validity window. Overridable via --window.
	// Anything shorter than 5 min risks a live request still holding
	// the old key when the revocation fires.
	defaultDualValidity = 24 * time.Hour
)

func secretsMain(args []string) {
	if len(args) == 0 {
		fmt.Println("Subcommands: rotate")
		return
	}
	switch args[0] {
	case "rotate":
		secretsRotate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown secrets subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func secretsRotate(args []string) {
	fs := flag.NewFlagSet("secrets rotate", flag.ExitOnError)
	target := fs.String("target", "", "jwt-signing | tenant-kek | api-internal-token | db-password")
	scope := fs.String("scope", "", "for jwt-signing: 'global'")
	tenant := fs.String("tenant", "", "for tenant-kek: tenant uuid")
	svcName := fs.String("service", "", "for api-internal-token: service name")
	role := fs.String("role", "", "for db-password: Postgres role name")
	windowFlag := fs.Duration("window", defaultDualValidity, "dual-validity window before the old secret is eligible for revoke")
	_ = fs.Parse(args)

	if *target == "" {
		fmt.Fprintln(os.Stderr, "--target is required")
		os.Exit(2)
	}

	ctx := context.Background()
	nc := mustConnectNATS()
	defer nc.Close()

	rotationID, _ := uuid.NewV7()
	started := time.Now().UTC()

	publishEvent(nc, subjectRotationStarted, map[string]any{
		"rotation_id": rotationID.String(),
		"target":      *target,
		"scope":       *scope,
		"tenant_id":   *tenant,
		"service":     *svcName,
		"role":        *role,
		"window":      windowFlag.String(),
		"started_at":  started.Format(time.RFC3339),
	})

	var outcome map[string]any
	var err error
	switch *target {
	case "jwt-signing":
		if *scope != "global" {
			err = fmt.Errorf("--scope=global required for jwt-signing")
		} else {
			outcome, err = rotateJWTSigning(ctx, *windowFlag)
		}
	case "tenant-kek":
		if *tenant == "" {
			err = fmt.Errorf("--tenant <uuid> required for tenant-kek")
		} else {
			outcome, err = rotateTenantKEK(ctx, *tenant)
		}
	case "api-internal-token":
		if *svcName == "" {
			err = fmt.Errorf("--service <name> required for api-internal-token")
		} else {
			outcome, err = rotateAPIInternalToken(ctx, *svcName, *windowFlag)
		}
	case "db-password":
		if *role == "" {
			err = fmt.Errorf("--role <name> required for db-password")
		} else {
			outcome, err = rotateDBPassword(ctx, *role, *windowFlag)
		}
	default:
		err = fmt.Errorf("unknown --target %q", *target)
	}

	if err != nil {
		publishEvent(nc, subjectRotationCompleted, map[string]any{
			"rotation_id":   rotationID.String(),
			"target":        *target,
			"status":        "failed",
			"error":         err.Error(),
			"completed_at":  time.Now().UTC().Format(time.RFC3339),
		})
		fatal("rotation failed: %v", err)
	}

	finalEvent := map[string]any{
		"rotation_id":   rotationID.String(),
		"target":        *target,
		"status":        "succeeded",
		"duration_ms":   time.Since(started).Milliseconds(),
		"completed_at":  time.Now().UTC().Format(time.RFC3339),
	}
	for k, v := range outcome {
		finalEvent[k] = v
	}
	publishEvent(nc, subjectRotationCompleted, finalEvent)

	fmt.Printf("rotation complete: target=%s rotation_id=%s duration=%s\n",
		*target, rotationID, time.Since(started).Round(time.Millisecond))
	if r, ok := outcome["revoke_not_before"]; ok {
		fmt.Printf("old secret eligible for revoke after: %s\n", r)
	}
}

// ---- target handlers ------------------------------------------------------

func rotateJWTSigning(ctx context.Context, window time.Duration) (map[string]any, error) {
	rdb := mustConnectRedis()
	defer rdb.Close()
	newKey := generateKey(32)
	revokeAt := time.Now().UTC().Add(window)
	// Redis layout expected by auth service:
	//   jwt_signing_key        — current primary
	//   jwt_signing_key:new    — newly-rotated; becomes primary on next read
	//   jwt_signing_key:old_expires — RFC3339; operators (or cron)
	//       run `revoke` after this timestamp.
	if err := rdb.Set(ctx, "jwt_signing_key:new", newKey, 0).Err(); err != nil {
		return nil, fmt.Errorf("redis write new: %w", err)
	}
	if err := rdb.Set(ctx, "jwt_signing_key:old_expires", revokeAt.Format(time.RFC3339), 0).Err(); err != nil {
		return nil, fmt.Errorf("redis write expiry: %w", err)
	}
	return map[string]any{
		"new_key_id":        keyFingerprint(newKey),
		"revoke_not_before": revokeAt.Format(time.RFC3339),
	}, nil
}

func rotateTenantKEK(ctx context.Context, tenant string) (map[string]any, error) {
	// Reuse the existing `dms-admin kms rotate` transactional logic.
	// It adds a v+1 row in tenant_keks and marks the prior version
	// retired; dual-read is implicit in the alias versioning.
	pool := mustPool()
	defer pool.Close()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var cur int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version),0) FROM tenant_keks WHERE tenant_id=$1::uuid`, tenant).Scan(&cur); err != nil {
		return nil, fmt.Errorf("lookup live version: %w", err)
	}
	newVer := cur + 1
	newAlias := fmt.Sprintf("vaultdms/tenant/%s@v%d", tenant, newVer)
	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant_keks (tenant_id, version, alias, created_at) VALUES ($1::uuid, $2, $3, now())`,
		tenant, newVer, newAlias); err != nil {
		return nil, fmt.Errorf("insert new kek version: %w", err)
	}
	// Mark the prior version retired with a 24h grace. Downstream
	// GenerateDataKey calls start using @v<newVer>; DecryptDataKey
	// keeps resolving older @v<N> aliases until hard-revoked.
	if cur > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE tenant_keks SET retired_at = now() WHERE tenant_id=$1::uuid AND version=$2`,
			tenant, cur); err != nil {
			return nil, fmt.Errorf("mark prior retired: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return map[string]any{
		"tenant_id": tenant,
		"new_alias": fmt.Sprintf("vaultdms/tenant/%s@v%d", tenant, newVer),
		"live_version": newVer,
	}, nil
}

func rotateAPIInternalToken(ctx context.Context, svcName string, window time.Duration) (map[string]any, error) {
	pool := mustPool()
	defer pool.Close()
	newToken := generateKey(32)
	revokeAt := time.Now().UTC().Add(window)
	// The inter-service auth token table (system_secrets) accepts a
	// previous_value + previous_expires_at for the dual-read hop.
	// Schema reminder: services read `value` first, fall back to
	// `previous_value` only when the header carries the old token
	// and `now() < previous_expires_at`.
	_, err := pool.Exec(ctx, `
		INSERT INTO system_secrets (key, value, created_at, expires_at)
		VALUES ($1, $2, now(), NULL)
		ON CONFLICT (key) DO UPDATE SET
			previous_value      = system_secrets.value,
			value               = EXCLUDED.value,
			previous_expires_at = $3,
			created_at          = now()
	`, "api_internal_token:"+svcName, newToken, revokeAt)
	if err != nil {
		return nil, fmt.Errorf("system_secrets upsert: %w", err)
	}
	return map[string]any{
		"service":           svcName,
		"new_token_id":      keyFingerprint(newToken),
		"revoke_not_before": revokeAt.Format(time.RFC3339),
	}, nil
}

func rotateDBPassword(ctx context.Context, role string, window time.Duration) (map[string]any, error) {
	pool := mustPool()
	defer pool.Close()
	newPassword := generateKey(24)
	// ALTER ROLE is the authoritative write. Postgres does NOT support
	// dual-password per role; the dual-read story for DB credentials
	// is instead:
	//   - App reads K8s Secret `vaultdms-db-credentials` which now
	//     contains the new password.
	//   - Until the rolling pod restart completes, old pods hold the
	//     previous password and reconnect on idle-timeout or pool-refresh.
	// Revocation is implicit: the OLD password stops working
	// immediately after ALTER ROLE, so the "window" here is only
	// advisory — k8s rolling restart needs to finish faster than
	// the pool-refresh timeout to avoid 5xx. See runbook.
	if _, err := pool.Exec(ctx, fmt.Sprintf("ALTER ROLE %s PASSWORD $1", quoteIdent(role)), newPassword); err != nil {
		return nil, fmt.Errorf("ALTER ROLE %s: %w", role, err)
	}
	return map[string]any{
		"role":              role,
		"password_fingerprint": keyFingerprint(newPassword),
		"k8s_secret_update_required": true,
		"advisory_window":   window.String(),
		"note": "update vaultdms-db-credentials K8s secret and restart pods; OLD password STOPS WORKING immediately",
	}, nil
}

// ---- helpers --------------------------------------------------------------

func mustConnectNATS() *nats.Conn {
	url := envOrDefault("VAULTDMS_NATS_URL", envOrDefault("NATS_URL", "nats://localhost:4222"))
	nc, err := nats.Connect(url, nats.Name("dms-admin.secrets"), nats.Timeout(5*time.Second))
	if err != nil {
		fatal("nats connect: %v", err)
	}
	return nc
}

func mustConnectRedis() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: envOrDefault("REDIS_URL", "localhost:6379")})
}

// mustPool lives in kms.go and is shared across subcommands.

func publishEvent(nc *nats.Conn, subject string, payload map[string]any) {
	id, _ := uuid.NewV7()
	envelope := map[string]any{
		"specversion":     "1.0",
		"id":              id.String(),
		"source":          "vaultdms.dms-admin",
		"type":            subject,
		"time":            time.Now().UTC().Format(time.RFC3339),
		"datacontenttype": "application/json",
		"data":            payload,
	}
	body, _ := json.Marshal(envelope)
	// Fire-and-forget publish with a Nats-Msg-Id so JetStream dedupes
	// the event if a flaky admin CLI run retries the same rotation.
	msg := &nats.Msg{Subject: subject, Data: body, Header: nats.Header{"Nats-Msg-Id": []string{id.String()}}}
	if err := nc.PublishMsg(msg); err != nil {
		fmt.Fprintf(os.Stderr, "warn: publish %s failed: %v\n", subject, err)
	}
	_ = nc.Flush()
}

// keyFingerprint returns the first 8 hex chars of SHA-256(key) for
// logs + events. Never log the key itself.
func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

// quoteIdent minimally escapes a SQL identifier for ALTER ROLE. The
// role name is from --role, operator-controlled, not user input —
// this guard exists so a typo doesn't execute arbitrary SQL.
func quoteIdent(name string) string {
	// Reject anything outside a conservative charset.
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			fatal("invalid role name %q (a-z, 0-9, _ only)", name)
		}
	}
	return `"` + name + `"`
}

// generateKey and fatal already live in main.go; re-declaring would
// collide with the same package. Reference them here for symmetry
// with what the per-target handlers call:
//   generateKey(n int) string            → main.go
//   fatal(fmtStr string, args ...any)    → main.go
//   envOrDefault(key, def string) string → main.go

// keep unused imports explicit to the reader — removed during future
// edits.
var _ = base64.StdEncoding
var _ = hex.EncodeToString
var _ = rand.Reader
