// LIC-1 — seat-limit enforcement on every user-creation path.
//
// The license JWT carries SeatLimit (pkg/license/claims.go). Before this
// commit, no caller checked it: tenants could add unlimited users
// regardless of plan, defeating the point of the license claim.
//
// enforceSeatLimit is called inside the same tx that does users.Create,
// so the count + the gate + the insert see a consistent snapshot. The
// counter (users.CountActive) counts active non-deleted users only —
// suspended / soft-deleted / pending-invite rows don't consume a seat,
// matching what billing dashboards typically show as "seats in use."
//
// Skip cases (gate returns nil — i.e. allow):
//
//   - License not loaded → unlicensed dev mode. The whole license
//     subsystem is opt-in; treating no-license as "infinite seats"
//     matches the rest of the gates that respect StatusUnlicensedDev.
//   - SeatLimit == 0 in the parsed claims → unlimited plan. Issuers
//     use 0 as a sentinel for enterprise/unlimited tiers; a negative
//     value would also be treated as unlimited but the JWT schema
//     constrains it to >= 0.
//
// Failure mode: at-or-above the cap returns vdmserr.Conflict (HTTP 409)
// with a stable message tail callers can pattern-match on for billing
// upgrade prompts: "seat limit reached: ".

package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/license"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// enforceSeatLimit gates user creation against the license's SeatLimit
// claim. Caller MUST run this inside the same tx as the subsequent
// users.Create so the count and the insert see a consistent snapshot
// of the users table.
func (s *Service) enforceSeatLimit(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error {
	claims := license.Current()
	if claims == nil {
		return nil // unlicensed dev mode — no enforcement
	}
	if claims.SeatLimit <= 0 {
		return nil // 0 / negative SeatLimit = unlimited plan
	}
	active, err := s.users.CountActive(ctx, tx, tenantID)
	if err != nil {
		return fmt.Errorf("seat limit check: %w", err)
	}
	if active >= claims.SeatLimit {
		return vdmserr.Conflict(fmt.Sprintf(
			"seat limit reached: tenant at %d/%d seats; upgrade plan to add more users",
			active, claims.SeatLimit,
		))
	}
	return nil
}
