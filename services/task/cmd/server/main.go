// Package main boots the SeDoc task service (task-assignment API).
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

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aieera/sedoc/pkg/config"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"

	"github.com/aieera/sedoc/services/task/internal/handler"
	"github.com/aieera/sedoc/services/task/internal/service"
)

const serviceName = "task"

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

	// ADR 0095 — license validation. Init reads SEDOC_LICENSE_JWT (or
	// /etc/vaultdms/license.jwt), verifies the RS256 signature against the
	// public key bundled in pkg/license/dev_pubkey.go, and caches the parsed
	// claims for license.Current() readers. Absent license is OK
	// (unlicensed_dev_mode); a present-but-invalid license is fatal, and
	// SEDOC_REQUIRE_LICENSE=true escalates absence to fatal too.
	if err := license.Init(); err != nil {
		log.Fatal(ctx).Err(err).Msg("license init")
	}
	license.StartReloader(ctx)

	// ---- Dependencies -------------------------------------------------------
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

	// ---- Policy gRPC client -------------------------------------------------
	// I-2: a document-scoped task list (filter=all&document_id=X) asks the
	// policy service whether a member may read X. POLICY_SERVICE_ADDR
	// (pkg/config, default policy:9090). Dialed lazily, as the GraphQL
	// gateway does: boot order does not matter and a policy restart heals
	// without restarting this service. Until it answers, those listings
	// fail closed (ErrPolicyUnavailable -> 500); nothing else here uses it.
	svc := service.New(pool, *log.Z())
	if policyConn := dialPolicy(ctx, log, cfg.PolicyServiceAddr); policyConn != nil {
		svc.Policy = sedocv1.NewPolicyServiceClient(policyConn)
		defer func() { _ = policyConn.Close() }()
	}

	// ---- Health ---------------------------------------------------------------
	hs := health.NewServerWithMeta(serviceName, cfg.Region, pool, rdb, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	// ---- HTTP (session-authenticated task API, no gRPC surface) ---------------
	httpMux := http.NewServeMux()
	h := handler.New(svc, *log.Z())
	h.Register(httpMux)
	httpRoot := apiHandler(httpMux, middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool}))
	// BUG-08 — response security headers, wrapped outermost so they also
	// land on the 401/403/429 responses written by the middleware below.
	secHeaders := middleware.SecurityHeaders(middleware.SecurityHeadersFromConfig(cfg))
	httpSrv := newHTTPServer(fmt.Sprintf(":%d", cfg.HTTPPort), secHeaders(middleware.RequireGatewaySignature()(httpRoot)))
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg(serviceName + " http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	// ---- Outbox publisher -------------------------------------------------------
	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)

	// ---- Notification sweep -----------------------------------------------------
	// ADR 0068 — hourly sweep. Stamps reminded_at / overdue_notified_at on
	// tasks crossing the 24h-out and overdue thresholds; emits one notify
	// event per claimed row. UPDATE…RETURNING makes the claim + emit pair
	// effectively idempotent (no double-fire on the next tick). Mirrors the
	// document service's equivalent block (cmd/server/main.go:1080-1100).
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		// Run once on boot so a deploy doesn't wait an hour to send the
		// first reminder after a cold start.
		_ = svc.SweepTaskNotifications(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := svc.SweepTaskNotifications(ctx); err != nil {
					log.Warn(ctx).Err(err).Msg("task notification sweep failed")
				}
			}
		}
	}()

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
	outbox.Stop()
}

// dialPolicy creates the (lazily connecting) policy gRPC client
// connection. nil when the address is empty or invalid — svc.Policy then
// stays nil and document-scoped listings fail closed.
func dialPolicy(ctx context.Context, log *logger.Logger, addr string) *grpc.ClientConn {
	if addr == "" {
		log.Warn(ctx).Msg("POLICY_SERVICE_ADDR empty; document-scoped task lists fail closed")
		return nil
	}
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Correlation id + client IP / UA into the policy decision log.
		grpc.WithChainUnaryInterceptor(middleware.ClientPropagationInterceptor()),
	)
	if err != nil {
		log.Warn(ctx).Err(err).Str("addr", addr).
			Msg("policy client init failed; document-scoped task lists fail closed")
		return nil
	}
	return conn
}

// Server timeouts (DEC-19).
const (
	readTimeout  = 60 * time.Second
	writeTimeout = 120 * time.Second
	idleTimeout  = 120 * time.Second
)

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// apiHandler wraps the task API: a 1 MiB body cap (DEC-19) ahead of
// SessionAuth, so an oversized body is refused before the session lookup.
func apiHandler(mux http.Handler, sessAuth func(http.Handler) http.Handler) http.Handler {
	return middleware.MaxBody(middleware.MaxJSONBody)(sessAuth(mux))
}
