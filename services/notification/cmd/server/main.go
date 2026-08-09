// Package main boots the SeDoc notification service.
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
	"github.com/aieera/sedoc/pkg/notifications"
	"github.com/aieera/sedoc/services/notification/internal/handler"
	"github.com/aieera/sedoc/services/notification/internal/repository"
	"github.com/aieera/sedoc/services/notification/internal/service"
)

const serviceName = "notification"

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

	// Wave 12.1: SMTP transactional email sender. Empty host → sender
	// reports Enabled()=false and service falls back to the pre-existing
	// "would send email" log line. Configure via SEDOC_SMTP_* envs.
	smtpSender := service.NewSMTPSender(service.SMTPConfig{
		Host:     cfg.SMTPHost,
		Port:     cfg.SMTPPort,
		Username: cfg.SMTPUsername,
		Password: cfg.SMTPPassword,
		From:     cfg.SMTPFrom,
		StartTLS: cfg.SMTPStartTLS,
	})
	svc := service.New(service.Config{Repo: repo, Redis: rdb, SMTP: smtpSender, Logger: *log.Z()})
	// ADR 0117 — mobile push via the Expo push service. Send only fires
	// for users with registered devices; no keys/config needed (Expo
	// fronts FCM+APNs). SEDOC_EXPO_PUSH_URL overrides the endpoint.
	svc.SetPushTransport(notifications.NewExpoClient(*log.Z()))

	// Per-tenant SMTP override (migration 000044). When a tenant has
	// saved their own SMTP creds via the admin UI Notifications tab,
	// outbound transactional mail uses those instead of the env-mode
	// smtpSender above.
	smtpSealKey := deriveSMTPSealKey(cfg.LocalKEK)
	svc.SetTenantSMTPDeps(repo, smtpSealKey)

	if err := svc.StartConsumer(ctx, js); err != nil {
		log.Fatal(ctx).Err(err).Msg("start consumer")
	}
	// Wave 0.2 — deliver role-targeted dms.notification.send.v1 events
	// emitted by the Python intelligence tasks (compliance_scan etc.).
	if err := svc.StartNotificationSendConsumer(ctx, js); err != nil {
		log.Fatal(ctx).Err(err).Msg("start notification.send consumer")
	}

	// ADR 0086 — 1-minute sweep that flushes ripe digest rows.
	// Cancels with the service lifecycle ctx; no separate Stop().
	go svc.StartDigestFlusher(ctx)

	hs := health.NewServerWithMeta("notification", cfg.Region, pool, rdb, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

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

	mux := http.NewServeMux()
	h := handler.New(svc, *log.Z())
	h.Register(mux)
	h.RegisterDevices(mux)
	// SessionAuthOptional resolves the caller from the session cookie OR
	// a Bearer session token (ADR 0117 — the mobile app) before the
	// header gap-fill. Kong strips client-supplied identity headers and
	// injects none (no session plugin yet), so without this the service
	// could not identify ANY external caller; IdentityHeadersHTTP stays
	// for trusted in-cluster callers that set the headers directly.
	// BUG-08 — response security headers, wrapped outermost so they also
	// land on the 401/403/429 responses written by the middleware below.
	secHeaders := middleware.SecurityHeaders(middleware.SecurityHeadersFromConfig(cfg))
	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: secHeaders(middleware.RequireGatewaySignature()(middleware.SessionAuthOptional(middleware.SessionAuthConfig{Pool: pool})(middleware.IdentityHeadersHTTP()(mux)))), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)
	_ = smtpSealKey // referenced indirectly via svc.SetTenantSMTPDeps above

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

// deriveSMTPSealKey returns a 32-byte AES key derived from the
// deployment KEK. Same SHA-256 + domain-separation pattern as the
// signature service's deriveSealingKey. Domain string is unique so a
// dump can't substitute keys across services.
func deriveSMTPSealKey(kek string) []byte {
	h := sha256.Sum256([]byte("vaultdms.notification.smtp.seal.v1:" + kek))
	return h[:]
}
