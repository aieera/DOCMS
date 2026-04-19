// Package main boots the VaultDMS auth service.
//
// Surface:
//   - HTTP (chi router): /api/v1/auth/{register, login, logout, mfa/*, sessions/*, api-keys/*}
//   - Session validation middleware exported via handler.AuthMiddleware
//     (imported directly by every other VaultDMS service that authenticates
//      requests at the edge)
//
// SSO (SAML/OIDC) and SCIM land in follow-up phases A2-A4.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vaultdms/vaultdms/pkg/config"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/events"
	"github.com/vaultdms/vaultdms/pkg/health"
	"github.com/vaultdms/vaultdms/pkg/logger"
	"github.com/vaultdms/vaultdms/pkg/middleware"

	"github.com/vaultdms/vaultdms/services/auth/internal/handler"
	"github.com/vaultdms/vaultdms/services/auth/internal/repository"
	"github.com/vaultdms/vaultdms/services/auth/internal/scim"
	"github.com/vaultdms/vaultdms/services/auth/internal/service"
	"github.com/vaultdms/vaultdms/services/auth/internal/sso"
)

const serviceName = "auth"

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

	// ---- Dependencies ------------------------------------------------------

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

	// MFA encryption key. In dev we read from VAULTDMS_LOCAL_KEK (base64).
	// In prod this gets swapped out for a KMS-backed KeyManager — service
	// accepts both via Config.KMS / Config.LocalKEK.
	localKEK, err := loadLocalKEK(cfg.LocalKEK)
	if err != nil {
		log.Warn(ctx).Err(err).Msg("local KEK unavailable; MFA setup will fail until configured")
	}

	// ---- Service ----------------------------------------------------------
	svc := service.New(service.Config{
		Pool:     pool,
		Redis:    rdb,
		Users:    repository.NewUserRepo(),
		Sessions: repository.NewSessionRepo(),
		APIKeys:  repository.NewAPIKeyRepo(),
		Outbox:   database.NewOutboxRepository(),
		LocalKEK: localKEK,
		Logger:   *log.Z(),
	})

	// ---- Handler ----------------------------------------------------------
	cookieSecure := cfg.Environment == "prod" || cfg.Environment == "staging"
	h := handler.New(handler.Config{
		Service:      svc,
		Logger:       *log.Z(),
		CookieSecure: cookieSecure,
	})

	// ---- SAML 2.0 (Phase A2) ----------------------------------------------
	// Dev generates a self-signed SP key+cert on every boot. Prod callers
	// should load from a secret store and pass via sso.LoadSPKeyMaterial.
	spKM, err := sso.NewSelfSignedSP()
	if err != nil {
		log.Warn(ctx).Err(err).Msg("saml sp init failed; SSO routes will be disabled")
	}
	var samlHandler *handler.SAMLHandler
	if spKM != nil {
		samlSvc := sso.NewService(sso.ServiceConfig{
			SP:          spKM,
			Pool:        pool,
			Redis:       rdb,
			ConfigRepo:  sso.NewConfigRepo(),
			Provisioner: svc,
			Logger:      *log.Z(),
			PublicURL:   cfg.PublicURL,
		})
		samlHandler = handler.NewSAMLHandler(samlSvc, h, pool)
	}

	// ---- OIDC (Phase A3) --------------------------------------------------
	// The OIDC service is always constructed; it only becomes active for a
	// tenant when that tenant has an sso_configs row with provider_type=oidc.
	oidcSvc := sso.NewOIDCService(sso.OIDCServiceConfig{
		Pool:        pool,
		Redis:       rdb,
		ConfigRepo:  sso.NewConfigRepo(),
		Provisioner: svc,
		Logger:      *log.Z(),
		PublicURL:   cfg.PublicURL,
	})
	oidcHandler := handler.NewOIDCHandler(oidcSvc, h, pool)

	// ---- SCIM 2.0 (Phase A4) ----------------------------------------------
	// Activated per-tenant via sso_configs.config.scim_token_hash. The
	// handler is always constructed; rows without a token hash yield 401s.
	publicURL := strings.TrimRight(cfg.PublicURL, "/")
	scimHandler := scim.NewHandler(scim.NewRepo(pool), publicURL+"/api/v1/scim/v2")
	scimResolver := scim.NewTenantResolver(pool)

	// ---- Health ------------------------------------------------------------
	hs := health.NewServer(pool, rdb, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	// ---- HTTP --------------------------------------------------------------
	// The chi router from h.Router() is the auth surface. We wrap it in the
	// platform middleware stack (correlation, requestlog, recovery, ratelimit).
	rl := middleware.NewRateLimiter(rdb, cfg.DefaultRateLimitPerMin)

	rootMux := middleware.RecoveryHTTP(log)(
		middleware.CorrelationHTTP(
			middleware.RequestLogHTTP(log)(
				middleware.RateLimitHTTP(rl, "auth")(h.Router(samlHandler, oidcHandler, &handler.SCIMWiring{
					Handler:  scimHandler,
					Resolver: scimResolver,
				}, handler.NewGroupsHandler(pool, *log.Z()),
					handler.NewSSOAdminHandler(pool, *log.Z()),
					handler.NewTenantAdminHandler(pool, *log.Z()))),
			),
		),
	)

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           rootMux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("auth http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	// ---- Outbox publisher --------------------------------------------------
	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
	outbox.Stop()
}

// loadLocalKEK decodes the base64 32-byte AES key previously sourced from
// cfg.LocalKEK. Dev/test use only — see pkg/crypto KeyManager for prod.
func loadLocalKEK(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("VAULTDMS_LOCAL_KEK not set")
	}
	k, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("expected 32 bytes, got %d", len(k))
	}
	return k, nil
}
