// Package main boots the VaultDMS acknowledgement service (Wave 15.1).
// Scope today: HTTP CRUD + ack + report, attestation via per-tenant
// HMAC signing key, hash-chained local audit + outbox emission.
// Temporal reminder / escalation workers land as a Wave 15.1
// follow-up (see docs/reports/WAVE_15_PROGRESS.md).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/vaultdms/vaultdms/pkg/config"
	"github.com/vaultdms/vaultdms/pkg/crypto"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/events"
	"github.com/vaultdms/vaultdms/pkg/health"
	"github.com/vaultdms/vaultdms/pkg/logger"
	"github.com/vaultdms/vaultdms/pkg/middleware"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/handler"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/repository"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/service"
)

const serviceName = "acknowledgement"

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

	pool, err := database.NewPool(ctx, cfg.DatabaseURL, database.DefaultPoolConfig())
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("postgres connect")
	}
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisURL, Password: cfg.RedisPassword, DB: cfg.RedisDB})
	defer func() { _ = rdb.Close() }()

	nc, js, err := events.ConnectNATS(cfg.NATSURL)
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("nats connect")
	}
	defer nc.Close()

	// KMS: LocalKeyManager in dev, swap to Vault/AWS/PKCS#11 via cfg.
	// MASTER secret lives in env; see pkg/config + ADR 0022.
	km, err := crypto.NewLocalKeyManager(cfg.LocalKEK, nil)
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("kms init")
	}

	repo := repository.New()
	outbox := database.NewOutboxRepository()

	// T-D-6 — Resolver is mandatory. The former nil-fallback silently
	// installed StaticRecipientResolver which only understands
	// policy.Users and rejects Groups/Roles as validation errors.
	// Operators learned about a missing real resolver only when an
	// admin created a campaign targeting groups. Now fail-fast:
	//
	//   VAULTDMS_ACK_RESOLVER=static   → dev/tests only; admins can
	//                                    only create users-only campaigns.
	//   VAULTDMS_ACK_RESOLVER=auth     → not yet implemented; reserved.
	//   (unset)                        → panic at boot.
	resolver, err := loadResolver(os.Getenv("VAULTDMS_ACK_RESOLVER"))
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("resolver init")
	}

	svc, err := service.New(service.Config{
		Pool:     pool,
		Repo:     repo,
		Outbox:   outbox,
		KMS:      km,
		Resolver: resolver,
		Logger:   *log.Z(),
	})
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("service init")
	}

	publisher := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go publisher.Start(ctx)
	defer publisher.Stop()

	hs := health.NewServer(pool, rdb, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	r := chi.NewRouter()
	// Global: gateway-signature + tenant-header resolution. Both
	// public + internal paths need these.
	r.Use(middleware.RequireGatewaySignature())
	r.Use(middleware.TenantHTTP(pool))

	h := handler.New(svc)
	// Internal worker routes — no session cookie, just the worker's
	// synthesised tenant header.
	h.RegisterInternal(r)
	// Public user-facing routes — session-cookie authenticated so
	// auth.User(ctx) resolves inside the handler.
	r.Group(func(r chi.Router) {
		r.Use(middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool}))
		h.RegisterPublic(r)
	})

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
}

// loadResolver picks a RecipientResolver by mode env var. Fails
// closed when unset — see T-D-6 rationale above the call site.
func loadResolver(mode string) (service.RecipientResolver, error) {
	switch mode {
	case "static":
		return service.StaticRecipientResolver{}, nil
	case "auth":
		return nil, fmt.Errorf("VAULTDMS_ACK_RESOLVER=auth: auth-service resolver not yet implemented (Wave 16)")
	case "":
		return nil, fmt.Errorf("VAULTDMS_ACK_RESOLVER is required; set to 'static' (dev) or 'auth' (prod)")
	default:
		return nil, fmt.Errorf("VAULTDMS_ACK_RESOLVER=%q: unknown mode (want 'static' or 'auth')", mode)
	}
}
