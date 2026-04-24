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
		// Cross-service HTTP targets. Empty = soft no-op.
		// Wave 12.4: search / qdrant / connector (DSR).
		// Wave 15.3 / 15.1: auth / acknowledgement (sweepers).
		ServiceURLs: map[string]string{
			"search":          os.Getenv("VAULTDMS_SEARCH_URL"),
			"qdrant":          os.Getenv("VAULTDMS_QDRANT_URL"),
			"connector":       os.Getenv("VAULTDMS_CONNECTOR_URL"),
			"auth":            os.Getenv("VAULTDMS_AUTH_URL"),
			"acknowledgement": os.Getenv("VAULTDMS_ACKNOWLEDGEMENT_URL"),
		},
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
	w.RegisterWorkflow(workflows.DeprovisionWorkflow)
	// Wave 15 sweepers.
	w.RegisterWorkflow(workflows.PasswordExpiryWorkflow)
	w.RegisterWorkflow(workflows.AckRemindersWorkflow)
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

	// Wave 15 per-tenant schedules: password-expiry (02:00 UTC) +
	// acknowledgement-reminders (09:00 UTC). Idempotent.
	if n, err := workflows.RegisterWave15Schedules(ctx, pool, tc, queue); err != nil {
		log.Error(ctx).Err(err).Msg("wave 15 schedules bootstrap failed")
	} else if n > 0 {
		log.Info(ctx).Int("created", n).Msg("wave 15 schedules registered")
	}

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatal(ctx).Err(err).Msg("temporal worker run")
	}
}
