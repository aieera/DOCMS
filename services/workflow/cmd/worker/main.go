// Package main boots a standalone Temporal worker for the VaultDMS
// workflow service.
//
// Wave 7 Prompt 7.1: the existing `cmd/server` binary embeds a worker
// alongside the HTTP/gRPC handler for dev convenience. Production
// deployments separate the two so worker pods can scale
// independently of the request-serving pods. This binary is the
// worker-only path: connect to Temporal, register all 5 workflows +
// all activities, block on `InterruptCh`.
//
// Taskqueue is `vaultdms-default` (configurable via
// VAULTDMS_WORKFLOW_TASK_QUEUE). Namespace is `vaultdms` per ADR
// 0023.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/vaultdms/vaultdms/pkg/config"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/logger"
	"github.com/vaultdms/vaultdms/services/workflow/internal/activities"
	"github.com/vaultdms/vaultdms/services/workflow/internal/workflows"
)

const (
	serviceName  = "workflow-worker"
	defaultQueue = "vaultdms-default"
	namespace    = "vaultdms"
)

var version = "dev"

func main() {
	cfg, err := config.Load(serviceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		os.Exit(1)
	}
	cfg.ServiceVersion = version

	log := logger.New(serviceName, cfg.ServiceVersion, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ---- Dependencies -----------------------------------------------------
	pool, err := database.NewPool(ctx, cfg.DatabaseURL, database.DefaultPoolConfig())
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("postgres connect")
	}
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisURL,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer func() { _ = rdb.Close() }()

	// ---- NATS / JetStream -------------------------------------------------
	// Optional dep for ADR 0068 saved-search alert event emission.
	// Failure here is logged-but-not-fatal: the worker stays up to
	// serve every other workflow, and EmitSavedSearchMatch returns a
	// typed error when JS is nil.
	var js nats.JetStreamContext
	if natsURL := os.Getenv("VAULTDMS_NATS_URL"); natsURL != "" {
		nc, nerr := nats.Connect(natsURL, nats.Name(serviceName))
		if nerr != nil {
			log.Warn(ctx).Err(nerr).Msg("nats connect; alert events will fail until restored")
		} else {
			defer nc.Drain()
			j, jerr := nc.JetStream()
			if jerr != nil {
				log.Warn(ctx).Err(jerr).Msg("jetstream init")
			} else {
				js = j
			}
		}
	}

	// ---- Temporal ---------------------------------------------------------
	tc, err := client.Dial(client.Options{
		HostPort:  cfg.TemporalAddr,
		Namespace: namespace,
	})
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("temporal connect")
	}
	defer tc.Close()

	queue := os.Getenv("VAULTDMS_WORKFLOW_TASK_QUEUE")
	if queue == "" {
		queue = defaultQueue
	}

	// ---- Worker + registrations ------------------------------------------
	acts := &activities.Activities{
		Pool:   pool,
		Outbox: database.NewOutboxRepository(),
		Redis:  rdb,
		// Wave 12.4: cross-service erase targets. Empty = soft no-op.
		ServiceURLs: map[string]string{
			"search":    os.Getenv("VAULTDMS_SEARCH_URL"),
			"qdrant":    os.Getenv("VAULTDMS_QDRANT_URL"),
			"connector": os.Getenv("VAULTDMS_CONNECTOR_URL"),
		},
		JS:  js,
		Log: *log.Z(),
	}
	w := worker.New(tc, queue, worker.Options{})
	w.RegisterWorkflow(workflows.ApprovalWorkflow)
	w.RegisterWorkflow(workflows.ParallelApprovalWorkflow)
	w.RegisterWorkflow(workflows.ReviewWorkflow)
	w.RegisterWorkflow(workflows.RetentionWorkflow)
	w.RegisterWorkflow(workflows.SignatureWorkflow)
	w.RegisterWorkflow(workflows.ExportWorkflow)
	w.RegisterWorkflow(workflows.EraseWorkflow)
	w.RegisterWorkflow(workflows.AnonymizeWorkflow)
	w.RegisterWorkflow(workflows.ResidencyMigrationWorkflow)
	// ADR 0068 — saved-search alert.
	w.RegisterWorkflow(workflows.SavedSearchAlertWorkflow)
	w.RegisterActivity(acts)

	log.Info(ctx).
		Str("namespace", namespace).
		Str("queue", queue).
		Str("version", version).
		Msg("workflow worker starting")

	// Bootstrap per-tenant retention schedules (Wave 8.1). Idempotent:
	// re-running against an existing schedule is a no-op. Failure here
	// is logged but NOT fatal — scheduling is a control-plane concern,
	// not a data-plane concern; letting the worker come up without
	// schedules beats crashlooping a worker that would otherwise
	// successfully serve on-demand workflows.
	if n, err := workflows.RegisterRetentionSchedules(ctx, pool, tc, queue); err != nil {
		log.Error(ctx).Err(err).Msg("retention schedules bootstrap failed")
	} else if n > 0 {
		log.Info(ctx).Int("created", n).Msg("retention schedules registered")
	}

	// ADR 0068 — bootstrap saved-search alert schedules. Same logged-
	// but-not-fatal contract as retention; the data plane keeps
	// serving even if Schedules can't register.
	if n, err := workflows.RegisterSavedSearchAlertSchedules(ctx, pool, tc, queue); err != nil {
		log.Error(ctx).Err(err).Msg("saved-search alert schedules bootstrap failed")
	} else if n > 0 {
		log.Info(ctx).Int("created", n).Msg("saved-search alert schedules registered")
	}

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatal(ctx).Err(err).Msg("temporal worker run")
	}
}
