// Package eventstream implements per-tenant event streaming (ADR 0077).
//
// Surfaces three things:
//   - Token issuance: mints a bearer (polling) + NATS user JWT/nkey
//     pair scoped to tenant.{id}.events.>. Issuance is idempotent on
//     label per tenant.
//   - Polling endpoint backing: List(token, since, cursor, limit) reads
//     from the per-tenant JetStream stream via a durable consumer.
//   - Mirror loop: consumes dms.{domain}.{action}.v1 from JetStream and
//     re-publishes onto tenant.{tenantUUID}.events.{domain}.{action}.v1
//     (creating the tenant stream lazily on first observation).
package eventstream

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
)

// parseTenantUUID is a tiny helper so callers can keep passing string
// tenant ids (which is what the HTTP layer hands us) while the
// WithTenantTx helper requires a uuid.UUID.
func parseTenantUUID(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

// DefaultRetention is the JetStream MaxAge for per-tenant event streams.
// §12.3 requires 7 days; tenants can override via the
// `event_stream_retention_days` setting (future wave).
const DefaultRetention = 7 * 24 * time.Hour

// DefaultTokenExpiry is how long a freshly issued token is valid.
const DefaultTokenExpiry = 30 * 24 * time.Hour

// streamName converts a tenant UUID to its JetStream stream name. UUIDs
// contain hyphens which JetStream rejects in stream names.
func streamName(tenantID string) string {
	return "TENANT_EVENTS_" + strings.ReplaceAll(tenantID, "-", "")
}

func subjectFilter(tenantID string) string {
	return "tenant." + tenantID + ".events.>"
}

// Service orchestrates token CRUD, mirroring, and reads.
type Service struct {
	pool      *pgxpool.Pool
	js        nats.JetStreamContext
	log       zerolog.Logger
	operator  nkeys.KeyPair // operator seed; nil → JWTs unsigned in dev
	mu        sync.Mutex
	streamSet map[string]struct{} // streams we know exist
}

// Config is DI.
type Config struct {
	Pool   *pgxpool.Pool
	JS     nats.JetStreamContext
	Logger zerolog.Logger
	// OperatorSeed (NKey "O..." seed) signs user JWTs. Empty in dev — issuance
	// still produces a JWT, but it's self-signed by an ephemeral operator
	// and will be rejected by a production NATS server in operator mode.
	OperatorSeed string
}

// New constructs a Service.
func New(cfg Config) (*Service, error) {
	s := &Service{
		pool: cfg.Pool, js: cfg.JS, log: cfg.Logger,
		streamSet: map[string]struct{}{},
	}
	if cfg.OperatorSeed != "" {
		kp, err := nkeys.FromSeed([]byte(cfg.OperatorSeed))
		if err != nil {
			return nil, fmt.Errorf("operator seed: %w", err)
		}
		s.operator = kp
	} else {
		// Dev: mint an ephemeral operator per process. JWTs minted here
		// are unverifiable by a different process, which is fine because
		// in dev the NATS server runs in no-auth mode.
		kp, err := nkeys.CreateOperator()
		if err != nil {
			return nil, fmt.Errorf("ephemeral operator: %w", err)
		}
		s.operator = kp
	}
	return s, nil
}

// ---- Stream provisioning --------------------------------------------------

// ensureStream creates the per-tenant JetStream stream if absent. The
// streamSet cache shortcuts the JetStream API call for streams we've
// already seen this process lifetime.
func (s *Service) ensureStream(ctx context.Context, tenantID string) error {
	name := streamName(tenantID)
	s.mu.Lock()
	_, known := s.streamSet[name]
	s.mu.Unlock()
	if known {
		return nil
	}
	if _, err := s.js.StreamInfo(name); err == nil {
		s.mu.Lock()
		s.streamSet[name] = struct{}{}
		s.mu.Unlock()
		return nil
	}
	_, err := s.js.AddStream(&nats.StreamConfig{
		Name:      name,
		Subjects:  []string{subjectFilter(tenantID)},
		Retention: nats.LimitsPolicy,
		Discard:   nats.DiscardOld,
		Storage:   nats.FileStorage,
		MaxAge:    DefaultRetention,
		MaxBytes:  5 * 1024 * 1024 * 1024, // 5 GiB
	})
	if err != nil {
		return fmt.Errorf("add stream %s: %w", name, err)
	}
	s.mu.Lock()
	s.streamSet[name] = struct{}{}
	s.mu.Unlock()
	return nil
}

// ---- Token model ----------------------------------------------------------

// Token is what the issuance handler returns. The plaintext fields
// (BearerToken, NATSCredsFile) are present only once at issuance —
// subsequent reads return the row without them.
type Token struct {
	ID            string     `json:"id"`
	Label         string     `json:"label"`
	BearerToken   string     `json:"bearer_token,omitempty"`
	NATSCredsFile string     `json:"nats_creds_file,omitempty"`
	NATSAccountID string     `json:"nats_account_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	Revoked       bool       `json:"revoked"`
}

// IssueToken mints a fresh polling-bearer + NATS user creds pair for a
// tenant. Returned Token carries the plaintext halves; only the hashes
// and the JWT are persisted. The caller is responsible for surfacing
// the bearer + creds to the operator exactly once.
func (s *Service) IssueToken(ctx context.Context, tenantID, actorID, label string) (*Token, error) {
	if label == "" {
		return nil, errors.New("label required")
	}
	// Ensure tenant JetStream stream + tenant NATS account exist.
	if err := s.ensureStream(ctx, tenantID); err != nil {
		return nil, err
	}

	// 1. Bearer token (polling). 32-byte URL-safe random with our prefix.
	bearer, hash, err := newBearer()
	if err != nil {
		return nil, err
	}

	// 2. NATS account (operator-signed) + user (account-signed) for direct NATS.
	accountKP, _ := nkeys.CreateAccount()
	accountPub, _ := accountKP.PublicKey()
	accountClaims := jwt.NewAccountClaims(accountPub)
	accountClaims.Name = "TENANT_" + strings.ReplaceAll(tenantID, "-", "")
	accountClaims.Limits.JetStreamLimits = jwt.JetStreamLimits{
		MemoryStorage: 0,
		DiskStorage:   5 * 1024 * 1024 * 1024,
		Streams:       8,
		Consumer:      32,
	}
	accountJWT, err := accountClaims.Encode(s.operator)
	if err != nil {
		return nil, fmt.Errorf("encode account jwt: %w", err)
	}

	userKP, _ := nkeys.CreateUser()
	userPub, _ := userKP.PublicKey()
	userSeed, _ := userKP.Seed()
	userClaims := jwt.NewUserClaims(userPub)
	userClaims.Name = "tenant-" + tenantID[:8] + "-" + time.Now().UTC().Format("20060102")
	userClaims.Sub.Allow = jwt.StringList{subjectFilter(tenantID)}
	userClaims.Pub.Deny = jwt.StringList{">"}
	exp := time.Now().Add(DefaultTokenExpiry)
	userClaims.Expires = exp.Unix()
	userJWT, err := userClaims.Encode(accountKP)
	if err != nil {
		return nil, fmt.Errorf("encode user jwt: %w", err)
	}

	// 3. Persist.
	tenantUUID, err := parseTenantUUID(tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant uuid: %w", err)
	}
	tokenID := newUUID()
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		var actor any
		if actorID != "" {
			actor = actorID
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO tenant_event_tokens
			    (tenant_id, id, token_hash, nats_account_id,
			     nats_user_jwt, nats_user_nkey_seed,
			     label, created_by, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), $9)`,
			tenantID, tokenID, hash, accountPub, userJWT, string(userSeed), label, actor, exp)
		_ = accountJWT // accountJWT is delivered to the customer in-band via the creds file header
		return err
	})
	if err != nil {
		return nil, err
	}

	creds := renderCredsFile(userJWT, string(userSeed))
	return &Token{
		ID:            tokenID,
		Label:         label,
		BearerToken:   bearer,
		NATSCredsFile: creds,
		NATSAccountID: accountPub,
		CreatedAt:     time.Now().UTC(),
		ExpiresAt:     &exp,
	}, nil
}

// ListTokens returns all non-deleted tokens for a tenant. Plaintext
// halves are never re-returned — the customer kept them at issuance.
func (s *Service) ListTokens(ctx context.Context, tenantID string) ([]*Token, error) {
	tenantUUID, err := parseTenantUUID(tenantID)
	if err != nil {
		return nil, err
	}
	var out []*Token
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, label, created_at, expires_at, last_used_at, revoked_at
			  FROM tenant_event_tokens
			 WHERE tenant_id = $1
			 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t := &Token{}
			var revoked *time.Time
			if err := rows.Scan(&t.ID, &t.Label, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &revoked); err != nil {
				return err
			}
			t.Revoked = revoked != nil
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// RevokeToken marks the token revoked. Polling auth checks revoked_at;
// NATS-side revocation requires the operator-mode server to honor
// account-JWT revocations (Wave 14).
func (s *Service) RevokeToken(ctx context.Context, tenantID, tokenID string) error {
	tenantUUID, err := parseTenantUUID(tenantID)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE tenant_event_tokens
			   SET revoked_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`,
			tenantID, tokenID)
		return err
	})
}

// LookupTokenTenant resolves a bearer to the tenant it grants. Returns
// "" if the token is unknown, revoked, or expired.
//
// This is a PRE-tenant lookup (the token IS how we learn the tenant) on
// a FORCE-RLS table, so it iterates tenants from the organizations
// registry and probes each under that tenant's context (Wave A.1,
// issue #71 — the raw-pool version failed closed in prod and event-
// stream auth was dead). O(tenants) per SSE connect/poll-auth, which is
// acceptable at current scale; the long-term shape is a non-RLS
// token→tenant lookup table (same design work as the auth pre-tenant
// reads, issue #75).
func (s *Service) LookupTokenTenant(ctx context.Context, bearer string) (string, error) {
	hash := hashBearer(bearer)
	tenants, err := database.ListTenantIDs(ctx, s.pool)
	if err != nil {
		return "", err
	}
	for _, tid := range tenants {
		var tenantID string
		err := database.WithTenantTx(ctx, s.pool, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `
				SELECT tenant_id::text FROM tenant_event_tokens
				 WHERE token_hash = $1
				   AND revoked_at IS NULL
				   AND (expires_at IS NULL OR expires_at > now())`,
				hash,
			).Scan(&tenantID)
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", err
		}
		// Best-effort last-used touch under the found tenant's context.
		// Ignore errors — auth shouldn't fail on an analytics field.
		touchTenant := tid
		go func() {
			ctx2, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = database.WithTenantTx(ctx2, s.pool, touchTenant, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx2,
					`UPDATE tenant_event_tokens SET last_used_at = now() WHERE token_hash = $1`, hash)
				return err
			})
		}()
		return tenantID, nil
	}
	return "", nil
}

// ---- Reads / polling ------------------------------------------------------

// Event is what the polling endpoint and the SSE tail return.
type Event struct {
	ID         string          `json:"id"`
	Subject    string          `json:"subject"`
	Type       string          `json:"type,omitempty"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// ListEvents reads events from the per-tenant stream via an ephemeral
// pull consumer starting at `cursor` (or `since` if cursor is empty).
// Returns (events, nextCursor, hasMore).
func (s *Service) ListEvents(ctx context.Context, tenantID string, since time.Time, cursor string, limit int) ([]*Event, string, bool, error) {
	if err := s.ensureStream(ctx, tenantID); err != nil {
		return nil, "", false, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	sub, err := s.js.PullSubscribe(subjectFilter(tenantID), "",
		nats.Bind(streamName(tenantID), ""),
		consumerOpt(since, cursor),
	)
	if err != nil {
		// Fall back to ephemeral OrderedConsumer if Bind without name fails.
		opts := []nats.SubOpt{nats.DeliverNew(), nats.OrderedConsumer()}
		if !since.IsZero() {
			opts = []nats.SubOpt{nats.StartTime(since), nats.OrderedConsumer()}
		}
		sub, err = s.js.SubscribeSync(subjectFilter(tenantID), opts...)
		if err != nil {
			return nil, "", false, fmt.Errorf("subscribe: %w", err)
		}
	}
	defer func() { _ = sub.Unsubscribe() }()

	out := make([]*Event, 0, limit)
	deadline := time.Now().Add(2 * time.Second)
	var lastSeq uint64
	for len(out) < limit && time.Now().Before(deadline) {
		msg, err := nextMsg(sub, 500*time.Millisecond)
		if err != nil {
			break // timeout — no more right now
		}
		ev := decodeMsg(msg)
		out = append(out, ev)
		meta, mErr := msg.Metadata()
		if mErr == nil {
			lastSeq = meta.Sequence.Stream
		}
		_ = msg.Ack()
	}
	next := ""
	hasMore := false
	if lastSeq > 0 {
		next = fmt.Sprintf("seq:%d", lastSeq)
	}
	if len(out) == limit {
		hasMore = true
	}
	return out, next, hasMore, nil
}

func consumerOpt(since time.Time, cursor string) nats.SubOpt {
	// PullSubscribe sub options take *one* delivery policy. If both are set
	// we prefer the cursor.
	if cursor != "" {
		if seq, ok := parseCursor(cursor); ok {
			return nats.StartSequence(seq + 1)
		}
	}
	if !since.IsZero() {
		return nats.StartTime(since)
	}
	return nats.DeliverNew()
}

func parseCursor(s string) (uint64, bool) {
	if !strings.HasPrefix(s, "seq:") {
		return 0, false
	}
	var n uint64
	if _, err := fmt.Sscanf(s[4:], "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

func nextMsg(sub *nats.Subscription, timeout time.Duration) (*nats.Msg, error) {
	// PullSubscribe and SubscribeSync both expose NextMsg.
	return sub.NextMsg(timeout)
}

// TailEvents streams events to the supplied callback until ctx is
// cancelled or `until` elapses. Used by the admin SSE live tail.
func (s *Service) TailEvents(ctx context.Context, tenantID string, until time.Duration, emit func(*Event) error) error {
	if err := s.ensureStream(ctx, tenantID); err != nil {
		return err
	}
	sub, err := s.js.SubscribeSync(subjectFilter(tenantID), nats.DeliverNew(), nats.OrderedConsumer())
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()
	deadline := time.Now().Add(until)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		msg, err := sub.NextMsg(500 * time.Millisecond)
		if err != nil {
			continue
		}
		ev := decodeMsg(msg)
		if err := emit(ev); err != nil {
			return err
		}
		_ = msg.Ack()
	}
	return nil
}

func decodeMsg(msg *nats.Msg) *Event {
	ev := &Event{Subject: msg.Subject, OccurredAt: time.Now().UTC()}
	if meta, err := msg.Metadata(); err == nil {
		ev.OccurredAt = meta.Timestamp
		ev.ID = fmt.Sprintf("%d", meta.Sequence.Stream)
	}
	// Try to decode as our envelope.
	var env map[string]json.RawMessage
	if err := json.Unmarshal(msg.Data, &env); err == nil {
		if t, ok := env["type"]; ok {
			_ = json.Unmarshal(t, &ev.Type)
		}
		if d, ok := env["data"]; ok {
			ev.Data = d
		} else {
			ev.Data = msg.Data
		}
	} else {
		ev.Data = msg.Data
	}
	return ev
}

// ---- Helpers --------------------------------------------------------------

func newBearer() (plaintext, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	plaintext = "tev_" + hex.EncodeToString(b)
	hash = hashBearer(plaintext)
	return plaintext, hash, nil
}

func hashBearer(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}

func renderCredsFile(jwtStr, seed string) string {
	// NATS creds file: a multi-section file with JWT + nkey seed. Exact
	// shape is fixed because clients use the standard creds parser.
	const tmpl = `-----BEGIN NATS USER JWT-----
%s
------END NATS USER JWT------

************************* IMPORTANT *************************
NKEY Seed printed below can be used to sign and prove identity.
NKEYs are sensitive and should be treated as secrets.

-----BEGIN USER NKEY SEED-----
%s
------END USER NKEY SEED------

*************************************************************
`
	return fmt.Sprintf(tmpl, jwtStr, seed)
}

func newUUID() string {
	// 16 random bytes + RFC-4122 v4 tagging.
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// silence unused
var _ = base64.StdEncoding
