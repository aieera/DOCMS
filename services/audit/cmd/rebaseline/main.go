// Command rebaseline is the one-time cutover of audit hash chains written
// before the canonical-hash fix (audit commit 07f13d71). Those chains hashed
// the raw event envelope while verification re-reads the stored jsonb, so
// they never verify — and the GDPR erasure path, which verifies before it
// redacts, refuses them with 409 audit_chain_unverifiable.
//
// For each tenant (or --tenant), under the tenant's chain lock, it recomputes
// every link with the verifier's own algorithm and reports how many differ.
// Nothing is written unless --apply is given. With --apply the differing
// rows are rewritten, an audit.chain_rebaselined event (old head, new head,
// count, operator) is appended, and a signed checkpoint
// (SEDOC_AUDIT_SIGNING_KEY, mandatory with --apply: exit 4 without it)
// anchors the new head, all in one transaction. A second run reports 0.
//
// A tenant whose chain already verifies is only re-signed when its latest
// checkpoint verifies and the chain still carries that signed prefix; a
// tenant with no checkpoint needs --allow-first-checkpoint. Anything else
// is refused (exit 5).
//
// It rewrites WORM rows, so it must run as the table owner / a BYPASSRLS
// operator role (the migration DSN). It refuses any other role.
// Runbook: docs/runbooks/audit-chain-rebaseline.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/audit/internal/repository"
	"github.com/aieera/sedoc/services/audit/internal/service"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rebaseline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsn := fs.String("dsn", os.Getenv("SEDOC_MIGRATE_DATABASE_URL"), "owner / BYPASSRLS Postgres DSN (default $SEDOC_MIGRATE_DATABASE_URL)")
	tenant := fs.String("tenant", "", "only this tenant (uuid); default every tenant")
	apply := fs.Bool("apply", false, "write the re-baseline (default: dry run, nothing written)")
	operator := fs.String("operator", "", "who runs this (recorded on the marker event; required with --apply)")
	cutoverFlag := fs.String("cutover", "", "RFC3339 deploy time of the canonical-hash fix (required with --apply); "+
		"a differing event at or after it refuses the tenant (exit 5)")
	allowFirst := fs.Bool("allow-first-checkpoint", false, "with --apply, sign a tenant that has no checkpoint at all "+
		"(incident-level decision: without it such a tenant is refused, exit 5, no_checkpoint=true)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dsn == "" {
		fmt.Fprintln(stderr, "rebaseline: --dsn (or SEDOC_MIGRATE_DATABASE_URL) is required")
		return 2
	}
	if *apply && *operator == "" {
		fmt.Fprintln(stderr, "rebaseline: --operator is required with --apply")
		return 2
	}
	var cutover *time.Time
	if *cutoverFlag != "" {
		c, err := time.Parse(time.RFC3339, *cutoverFlag)
		if err != nil {
			fmt.Fprintf(stderr, "rebaseline: --cutover: %v\n", err)
			return 2
		}
		cutover = &c
	}
	if *apply && cutover == nil {
		fmt.Fprintln(stderr, "rebaseline: --cutover (deploy time of the canonical-hash fix, RFC3339) is required with --apply")
		return 2
	}

	// A plain pool: the service pool's RLS posture gate refuses exactly the
	// role this tool needs.
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		fmt.Fprintf(stderr, "rebaseline: connect: %v\n", err)
		return 1
	}
	defer pool.Close()

	var bypass bool
	var role string
	if err := pool.QueryRow(ctx,
		`SELECT current_user, rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).
		Scan(&role, &bypass); err != nil {
		fmt.Fprintf(stderr, "rebaseline: role check: %v\n", err)
		return 1
	}
	if !bypass {
		fmt.Fprintf(stderr, "rebaseline: role %q is not BYPASSRLS; run with the migration/owner DSN, never the app role\n", role)
		return 3
	}

	// A re-baselined chain must be anchored by a signed checkpoint, or its
	// whole tail is uncertified: --apply refuses to start without a valid
	// signing key, before any tenant is touched.
	log := zerolog.New(stderr).With().Timestamp().Str("tool", "audit-rebaseline").Logger()
	svc := service.New(service.Config{
		Repo:              repository.New(pool),
		Logger:            log,
		SigningKeySeedB64: os.Getenv("SEDOC_AUDIT_SIGNING_KEY"),
	})
	if !svc.SigningKey().Enabled {
		if *apply {
			fmt.Fprintln(stderr, "rebaseline: --apply requires a valid SEDOC_AUDIT_SIGNING_KEY (the audit service's base64 Ed25519 seed); refusing before touching any tenant")
			return 4
		}
		fmt.Fprintln(stderr, "rebaseline: note: SEDOC_AUDIT_SIGNING_KEY is unset or invalid; this dry run writes nothing, but --apply will require SEDOC_AUDIT_SIGNING_KEY")
	}

	var tenants []uuid.UUID
	if *tenant != "" {
		id, err := uuid.Parse(*tenant)
		if err != nil {
			fmt.Fprintf(stderr, "rebaseline: --tenant: %v\n", err)
			return 2
		}
		tenants = []uuid.UUID{id}
	} else if tenants, err = database.ListTenantIDs(ctx, pool); err != nil {
		fmt.Fprintf(stderr, "rebaseline: %v\n", err)
		return 1
	}

	mode := "dry-run"
	if *apply {
		mode = "apply"
	}
	failed, refused := 0, 0
	for _, tid := range tenants {
		res, err := svc.Rebaseline(ctx, tid.String(), service.RebaselineOptions{
			Apply: *apply, Operator: *operator, Cutover: cutover, AllowFirstCheckpoint: *allowFirst,
		})
		fmt.Fprintf(stdout, "%s %s\n", mode, describe(tid.String(), res))
		switch {
		case errors.Is(err, repository.ErrDifferingAfterCutover), errors.Is(err, service.ErrUncertifiedHead):
			refused++
			log.Error().Err(err).Str("tenant", tid.String()).
				Msg("tenant REFUSED: investigate, do not re-baseline")
			fmt.Fprintf(stderr, "rebaseline: tenant %s refused: %v\n", tid, err)
			continue
		case err != nil:
			failed++
			log.Error().Err(err).Str("tenant", tid.String()).Msg("rebaseline failed")
			continue
		}
		log.Info().Str("tenant", tid.String()).Str("mode", mode).Int("events", res.Events).
			Int("differing", res.Rewritten).Int("at_or_after_cutover", res.AtOrAfterCutover).
			Bool("applied", res.Applied).Bool("checkpointed", res.Checkpointed).
			Str("old_head", res.OldHead).Str("new_head", res.NewHead).Msg("tenant processed")
	}
	if refused > 0 {
		fmt.Fprintf(stderr, "rebaseline: %d tenant(s) refused (differing events at or after the cutover, a head the signed history does not vouch for, "+
			"or no checkpoint without --allow-first-checkpoint) — investigate, do not re-baseline\n", refused)
		return 5
	}
	if failed > 0 {
		fmt.Fprintf(stderr, "rebaseline: %d tenant(s) failed\n", failed)
		return 1
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return 1
	}
	return 0
}

// describe renders one tenant's result on one line: counts, the first and
// last differing event (id@created_at) and, with a cutover, how many differ
// at or after it.
func describe(tenant string, r service.RebaselineResult) string {
	line := fmt.Sprintf("tenant=%s events=%d differing=%d", tenant, r.Events, r.Rewritten)
	if r.First != nil {
		line += fmt.Sprintf(" first=%s@%s last=%s@%s",
			r.First.ID, r.First.CreatedAt.Format(time.RFC3339Nano), r.Last.ID, r.Last.CreatedAt.Format(time.RFC3339Nano))
	}
	if r.CutoverConsidered {
		line += fmt.Sprintf(" at_or_after_cutover=%d", r.AtOrAfterCutover)
	}
	line += fmt.Sprintf(" applied=%t checkpointed=%t", r.Applied, r.Checkpointed)
	if r.NoCheckpoint {
		line += " no_checkpoint=true"
	}
	return line
}
