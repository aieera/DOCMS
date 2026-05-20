// Package main boots the VaultDMS billing service: tenant provisioning,
// Stripe webhooks, usage metering, feature flags.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"

	"github.com/vaultdms/vaultdms/pkg/config"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/events"
	"github.com/vaultdms/vaultdms/pkg/logger"
	"github.com/vaultdms/vaultdms/pkg/middleware"
	"github.com/vaultdms/vaultdms/services/billing/internal/flags"
	"github.com/vaultdms/vaultdms/services/billing/internal/handler"
	"github.com/vaultdms/vaultdms/services/billing/internal/metering"
	"github.com/vaultdms/vaultdms/services/billing/internal/provisioner"
	"github.com/vaultdms/vaultdms/services/billing/internal/repository"
	"github.com/vaultdms/vaultdms/services/billing/internal/service"
	stripehandler "github.com/vaultdms/vaultdms/services/billing/internal/stripe"
)

const serviceName = "billing"

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

	repo := repository.New(pool)
	// ADR 0110 — pass the cluster region so cross-region provision
	// attempts (e.g. a US Stripe webhook firing at the UAE cluster)
	// fail fast with a clear error instead of writing the org row
	// into the wrong region's Postgres.
	prov := provisioner.NewWithRegion(cfg.Region, repo, pool, rdb, *log.Z())
	flagChecker := flags.NewChecker(repo, rdb)
	stripeWH := stripehandler.NewWebhookHandler(repo, rdb, *log.Z(), cfg.StripeWebhookSecret)

	svc := service.New(service.Config{
		Repo: repo, Provisioner: prov, Flags: flagChecker, Logger: *log.Z(),
	})

	// ---- Usage metering (hourly cron) ------------------------------------
	meter := metering.New(repo, *log.Z())
	go meter.Start(ctx)
	defer meter.Stop()

	// ---- Grace period enforcer -------------------------------------------
	go enforceGracePeriod(ctx, repo, rdb, *log.Z())

	// ---- Health ----------------------------------------------------------
	// /healthz is liveness only (process is up). /readyz exercises the
	// billing repo's target table so schema-vs-code drift surfaces as 503
	// instead of silent per-request failures.
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"alive"}`))
	})
	healthMux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		probeCtx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		defer cancel()
		var n int
		if err := pool.QueryRow(probeCtx, `SELECT COUNT(*) FROM subscriptions`).Scan(&n); err != nil {
			http.Error(w, "not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	healthSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HealthPort), Handler: healthMux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := healthSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	// ---- gRPC (empty for now, kept for inter-service discovery) ----------
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(
		middleware.RecoveryInterceptor(log),
		middleware.CorrelationInterceptor(),
		middleware.TenantInterceptor(pool),
		middleware.UserIdentityInterceptor(),
		middleware.RequestLogInterceptor(log),
	))
	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("grpc listen")
	}
	go func() {
		log.Info(ctx).Int("port", cfg.GRPCPort).Msg("grpc listening")
		if err := grpcSrv.Serve(grpcLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Error(ctx).Err(err).Msg("grpc serve")
		}
	}()

	// ---- HTTP ------------------------------------------------------------
	// Two surfaces on the same port:
	//   /internal/v1/*         → service-to-service, X-API-Key auth
	//   /api/v1/admin/settings → user-facing, session + owner role
	coreMux := http.NewServeMux()
	h := handler.New(svc, stripeWH, *log.Z(), cfg.InternalAPIKey)
	h.Register(coreMux)

	adminMux := http.NewServeMux()
	h.RegisterAdmin(adminMux)
	var adminRoot http.Handler = adminMux
	adminRoot = middleware.RequireRole("owner")(adminRoot)
	adminRoot = middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(adminRoot)

	mux := http.NewServeMux()
	mux.Handle("/internal/", coreMux)
	mux.Handle("/api/v1/admin/settings", adminRoot)
	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: middleware.RequireGatewaySignature()(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	// ---- Outbox ----------------------------------------------------------
	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	grpcSrv.GracefulStop()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = healthSrv.Shutdown(shutdownCtx)
	outbox.Stop()
}

// enforceGracePeriod checks hourly for tenants past their grace period
// and suspends them.
func enforceGracePeriod(ctx context.Context, repo *repository.Repository, _ *redis.Client, log zerolog.Logger) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tenants, _ := repo.ListAllTenants(ctx)
			now := time.Now().UTC()
			for _, tid := range tenants {
				sub, err := repo.GetSubscription(ctx, tid)
				if err != nil || sub == nil {
					continue
				}
				if sub.Status == "past_due" && sub.GracePeriodEnds != nil && now.After(*sub.GracePeriodEnds) {
					_ = repo.UpdateSubscriptionStatus(ctx, tid, "suspended", nil)
					_ = repo.SuspendOrg(ctx, tid)
					log.Warn().Str("tenant", tid).Msg("grace period expired, tenant suspended")
				}
			}
		}
	}
}
