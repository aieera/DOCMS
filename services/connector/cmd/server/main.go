// Package main boots the SeDoc connector service — webhooks, MCP server,
// and OAuth connectors for external systems.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/config"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/services/connector/internal/email"
	"github.com/aieera/sedoc/services/connector/internal/eventstream"
	"github.com/aieera/sedoc/services/connector/internal/handler"
	"github.com/aieera/sedoc/services/connector/internal/ingest"
	"github.com/aieera/sedoc/services/connector/internal/intake"
	"github.com/aieera/sedoc/services/connector/internal/repository"
	"github.com/aieera/sedoc/services/connector/internal/service"
	"github.com/aieera/sedoc/services/connector/internal/webhook"
)

const serviceName = "connector"

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

	pool, err := database.NewPool(ctx, cfg.DatabaseURL, database.DefaultPoolConfig())
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("postgres connect")
	}
	defer pool.Close()
	// FIX-7 follow-up: RLS posture gate. Refuses to start when the
	// connection role unexpectedly has BYPASSRLS; set
	// SEDOC_ALLOW_BYPASS_RLS=1 in dev to opt in.

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisURL, Password: cfg.RedisPassword, DB: cfg.RedisDB})
	defer func() { _ = rdb.Close() }()

	nc, js, err := events.ConnectNATS(cfg.NATSURL)
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("nats connect")
	}
	defer nc.Close()

	repo := repository.New(pool)
	svc := service.New(service.Config{Repo: repo, Logger: *log.Z(), AllowPrivateWebhookTargets: cfg.WebhookAllowPrivateTargets})

	// Native connector wiring (ADR 0089). Sealing key and state-HMAC
	// secret are both derived from SEDOC_LOCAL_KEK with distinct
	// domain prefixes so a dump can't substitute one for the other.
	connSeal := deriveKey("vaultdms.connector.config.seal.v1:" + cfg.LocalKEK)
	connHMAC := deriveKey("vaultdms.connector.oauth.hmac.v1:" + cfg.LocalKEK)
	defaultRedirect := strings.TrimRight(cfg.PublicURL, "/") + "/api/v1/connectors/oauth/callback"
	if cfg.PublicURL == "" {
		defaultRedirect = "http://localhost:3000/api/v1/connectors/oauth/callback"
	}
	svc.SetConnectorDeps(connSeal, connHMAC, defaultRedirect)

	// Server-side ingest path. Dials storage gRPC + reuses the document REST
	// surface. Shared by Drive import (ADR 0089), email ingestion (ADR 0087),
	// and watched-folder intake (ADR 0088) so all three create real
	// documents-with-versions. Non-fatal: if storage is unreachable at boot
	// the import endpoint returns 503 and email/intake record per-item
	// failures rather than taking the whole connector down.
	var ingestClient *ingest.Client
	if ic, ierr := ingest.New(pool, *log.Z()); ierr != nil {
		log.Warn(ctx).Err(ierr).Msg("ingest client init failed; drive import will 503, email/intake skip materialise")
	} else {
		ingestClient = ic
		svc.SetIngestClient(ingestClient)
		defer func() { _ = ingestClient.Close() }()
	}

	// Start NATS event fanout → webhook deliveries.
	if err := svc.StartEventFanout(ctx, js); err != nil {
		log.Fatal(ctx).Err(err).Msg("event fanout")
	}

	// Start webhook delivery worker. The kicker hookup is what lets
	// test-send / redeliver hit the wire within sub-second instead
	// of waiting for the next 5 s poll tick.
	deliveryWorker := webhook.NewDeliveryWorker(repo, *log.Z())
	svc.SetWorkerKicker(deliveryWorker.Kick)
	go deliveryWorker.Start(ctx)
	defer deliveryWorker.Stop()

	// ADR 0077 — per-tenant event streaming. The operator seed signs
	// the per-tenant account JWTs handed back at token-issuance time;
	// empty means dev mode (ephemeral operator, JWTs minted but the
	// dev NATS server doesn't verify them — same shape as the
	// SEDOC_LOCAL_KEK convention).
	esSvc, err := eventstream.New(eventstream.Config{
		Pool:         pool,
		JS:           js,
		Logger:       *log.Z(),
		OperatorSeed: os.Getenv("SEDOC_NATS_OPERATOR_SEED"),
	})
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("eventstream init")
	}
	if err := esSvc.StartMirror(ctx); err != nil {
		log.Fatal(ctx).Err(err).Msg("eventstream mirror")
	}
	esHandler := handler.NewEventStreamHandler(esSvc)

	// ADR 0087 — email ingestion. Each polled envelope's body + attachments
	// are materialised into real documents-with-versions via the shared
	// ingest pipeline (so they OCR + embed + become searchable). Pollers run
	// with nil backends until real Graph/Gmail/IMAP credentials are wired.
	emailSvc := email.New(pool, nc, ingestClient, []email.Poller{
		&email.MicrosoftPoller{},
		&email.GmailPoller{},
		&email.IMAPPoller{Backend: &email.IMAPBackend{Pool: pool, Log: *log.Z()}},
	}, *log.Z())
	go emailSvc.Start(ctx)
	emailHandler := handler.NewEmailHandler(emailSvc)

	// ADR 0088 — watched-folder intake. Uses the same ingest pipeline as
	// email + Drive import, so dropped files become real documents-with-
	// versions. The supervisor reconciles every 60s; per-folder fsnotify
	// watcher runs in a goroutine, with a 30s poll fallback.
	intakeSvc := intake.New(pool, ingestClient, *log.Z())
	go intakeSvc.Start(ctx)
	intakeHandler := handler.NewIntakeHandler(intakeSvc)

	// MCP server extracted to services/mcp-server (ADR 0091).

	// ---- Health ----------------------------------------------------------
	hs := health.NewServerWithMeta("connector", cfg.Region, pool, rdb, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	// ---- gRPC ------------------------------------------------------------
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

	// ---- HTTP REST -------------------------------------------------------
	mux := http.NewServeMux()
	h := handler.New(svc, *log.Z())
	h.Register(mux)
	esHandler.Register(mux)
	emailHandler.Register(mux)
	intakeHandler.Register(mux)
	// FIX-1 follow-up: SessionAuthOptional so handlers read tenant/
	// user from ctx; Kong now strips the X-Auth-Tenant-ID + X-User-*
	// headers that connector handlers previously trusted.
	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: middleware.RequireGatewaySignature()(middleware.SessionAuthOptional(middleware.SessionAuthConfig{Pool: pool})(mux)), ReadHeaderTimeout: 5 * time.Second}
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
	_ = hs.Shutdown(shutdownCtx)
	outbox.Stop()
}

// deriveKey returns a 32-byte AES key from a domain-prefixed seed.
// Same pattern as the signature service's deriveSealingKey — distinct
// domain strings prevent cross-purpose key reuse if a dump leaks.
func deriveKey(seed string) []byte {
	h := sha256.Sum256([]byte(seed))
	return h[:]
}
