// dms-admin outbox — transactional-outbox operations.
//
// Subcommands:
//
//	dms-admin outbox replay-dlq [--tenant <uuid>] [--limit N] [--execute]
//
// replay-dlq moves dead-lettered events (outbox_dlq) back into the outbox
// with attempts reset to 0, so every service's publisher retries them on
// its next poll. It keeps the event id (JetStream deduplicates on it).
// Dry-run by default; --execute moves the rows. Without --tenant it walks
// every tenant in the organizations registry, oldest dead letters first.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/database"
)

func outboxMain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: dms-admin outbox <subcommand>")
		fmt.Fprintln(stderr, "Subcommands: replay-dlq")
		return 2
	}
	switch args[0] {
	case "replay-dlq":
		return outboxReplayDLQ(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown outbox subcommand: %s\n", args[0])
		return 2
	}
}

func outboxReplayDLQ(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("outbox replay-dlq", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tenant := fs.String("tenant", "", "replay only this tenant's dead letters (default: every tenant)")
	limit := fs.Int("limit", 100, "max rows to replay in this run")
	execute := fs.Bool("execute", false, "actually move the rows; default is a dry run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opts := database.ReplayDLQOptions{Limit: *limit, DryRun: !*execute}
	if *tenant != "" {
		id, err := uuid.Parse(*tenant)
		if err != nil || id == uuid.Nil {
			fmt.Fprintf(stderr, "invalid --tenant %q: must be a non-zero uuid\n", *tenant)
			return 2
		}
		opts.TenantID = id
	}
	if *limit <= 0 {
		fmt.Fprintln(stderr, "--limit must be positive")
		return 2
	}

	pool := mustPool()
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	rows, err := database.ReplayDLQ(ctx, pool, opts)
	fmt.Fprintf(stdout, "%-38s %-38s %-40s %4s  %s\n", "EVENT_ID", "TENANT_ID", "EVENT_TYPE", "ATT", "LAST_ERROR")
	for _, r := range rows {
		fmt.Fprintf(stdout, "%-38s %-38s %-40s %4d  %s\n", r.ID, r.TenantID, r.EventType, r.Attempts, trimBody([]byte(r.LastError)))
	}
	if err != nil {
		fmt.Fprintf(stderr, "replay-dlq: %v\n", err)
		return 1
	}
	if opts.DryRun {
		fmt.Fprintf(stdout, "\n%d dead-lettered event(s) selected (cap %d). Dry run — no changes. Pass --execute to replay.\n", len(rows), *limit)
		return 0
	}
	fmt.Fprintf(stdout, "\nreplayed %d event(s) into the outbox with attempts reset to 0.\n", len(rows))
	return 0
}
