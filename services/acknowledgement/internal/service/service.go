// Package service is the acknowledgement business-logic layer. The
// service owns (a) campaign lifecycle, (b) assignment resolution and
// acknowledgement, (c) HMAC attestation, and (d) the per-campaign
// hash chain stored in acknowledgement_events.
//
// This file holds only the Service struct, its Config/New, and the
// RecipientResolver seam. Concrete responsibilities are split into
// sibling files keyed by seam:
//
//   campaign_service.go    — Create/Get/List/Close
//   assignment_service.go  — Acknowledge / MyPending / Report
//   attestation_service.go — per-tenant HMAC key + verification
//   sweeper_service.go     — reminder + escalation sweep
//   event_service.go       — outbox subjects, hash-chain append,
//                            notification fan-out, canonical JSON
//
// Invariants enforced by this layer (see each sub-file for the
// specifics that motivate them):
//   - Only users.role IN ('compliance_officer','admin','owner') can
//     create campaigns (caller enforces at the handler/OPA layer;
//     service reads the role from context and rejects otherwise).
//   - attestation_hash = HMAC-SHA256(tenant_kek, campaign_id || "|" ||
//     assignee_id || "|" || RFC3339Nano(acknowledged_at)).
//   - acknowledgement_events is append-only; each row's self_hash =
//     SHA-256(prev_hash || canonical(payload)). The FIRST row's
//     prev_hash is NULL.
//   - Outbox emission for every state change (campaign.created,
//     acknowledged, closed). Reminder + escalation events land when
//     the Temporal schedule wires up — tracked as Wave 15.1 follow-up.
package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/crypto"
	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/model"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/repository"
)

// RecipientResolver turns a RecipientPolicy into a flat list of user
// IDs. Injected so the service doesn't bring the auth/identity
// service in as a hard dep; wire-up in cmd/server/main.go.
type RecipientResolver interface {
	Resolve(ctx context.Context, tenantID uuid.UUID, policy model.RecipientPolicy) ([]uuid.UUID, error)
}

// StaticRecipientResolver is used in dev/tests: returns policy.Users
// verbatim and ignores Groups/Roles. Production wiring supplies a
// real resolver that queries groups + role mappings.
type StaticRecipientResolver struct{}

// Resolve returns the explicit users list only — groups/roles are a
// follow-up (see WAVE_15_PROGRESS.md).
func (StaticRecipientResolver) Resolve(_ context.Context, _ uuid.UUID, p model.RecipientPolicy) ([]uuid.UUID, error) {
	if len(p.Groups) > 0 || len(p.Roles) > 0 {
		// Don't silently drop; caller should see the gap.
		return nil, vdmserr.Validation("recipient_policy",
			"group/role resolution not yet wired; supply users[] explicitly")
	}
	out := make([]uuid.UUID, 0, len(p.Users))
	seen := make(map[uuid.UUID]struct{}, len(p.Users))
	for _, u := range p.Users {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out, nil
}

// Service bundles deps. Methods live across the *_service.go files in
// this package; receivers stay on *Service so the public API is flat.
type Service struct {
	pool     *pgxpool.Pool
	repo     repository.Repo
	outbox   *database.OutboxRepository
	kms      crypto.KeyManager
	resolver RecipientResolver
	log      zerolog.Logger
	now      func() time.Time

	// keyCache holds plaintext HMAC keys keyed by tenant. Populated
	// lazily on first use after unwrapping the stored wrapped_key;
	// cleared on rotation. Protected by a RWMutex rather than a
	// sync.Map so we can truncate en masse during rotation.
	keyCacheMu sync.RWMutex
	keyCache   map[uuid.UUID][]byte
}

// Config bundles dependencies.
type Config struct {
	Pool     *pgxpool.Pool
	Repo     repository.Repo
	Outbox   *database.OutboxRepository
	KMS      crypto.KeyManager
	Resolver RecipientResolver
	Logger   zerolog.Logger
}

// New constructs a service. Returns an error when cfg.Resolver is
// nil — T-D-6. The former StaticRecipientResolver silent fallback
// masked a production misconfig (auth-service resolver missing) as a
// per-request validation error, so operators learned only when an
// admin created a campaign targeting groups. This path now fails at
// boot so the operator sees it immediately.
//
// Tests that want the dev/static path construct
// StaticRecipientResolver{} explicitly and pass it in.
func New(cfg Config) (*Service, error) {
	if cfg.Resolver == nil {
		return nil, fmt.Errorf("acknowledgement.service.New: Resolver is required (supply StaticRecipientResolver{} in tests; wire an auth-service resolver in prod)")
	}
	return &Service{
		pool: cfg.Pool, repo: cfg.Repo, outbox: cfg.Outbox,
		kms: cfg.KMS, resolver: cfg.Resolver, log: cfg.Logger,
		now:      time.Now,
		keyCache: map[uuid.UUID][]byte{},
	}, nil
}

// clock returns the service's current UTC time; every wall-clock read
// in this package must go through this seam so tests can freeze time.
func (s *Service) clock() time.Time { return s.now().UTC() }
