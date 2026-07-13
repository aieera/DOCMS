// Package main boots the SeDoc auth service.
//
// Surface:
//   - HTTP (chi router): /api/v1/auth/{register, login, logout, mfa/*, sessions/*, api-keys/*}
//   - Session validation middleware exported via handler.AuthMiddleware
//     (imported directly by every other SeDoc service that authenticates
//     requests at the edge)
//
// SSO (SAML/OIDC) and SCIM land in follow-up phases A2-A4.
package main

import (
	"context"
	"crypto/sha256"
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

	"github.com/aieera/sedoc/pkg/config"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"

	"github.com/aieera/sedoc/pkg/notifications"

	"github.com/aieera/sedoc/services/auth/internal/handler"
	"github.com/aieera/sedoc/services/auth/internal/ldap"
	"github.com/aieera/sedoc/services/auth/internal/repository"
	"github.com/aieera/sedoc/services/auth/internal/scim"
	"github.com/aieera/sedoc/services/auth/internal/service"
	"github.com/aieera/sedoc/services/auth/internal/sso"

	"math/rand"
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

	// ---- Dependencies ------------------------------------------------------

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

	// MFA encryption key. In dev we read from SEDOC_LOCAL_KEK (base64).
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
		WebAuthn: repository.NewWebAuthnRepo(),
		Outbox:   database.NewOutboxRepository(),
		LocalKEK: localKEK,
		Logger:   *log.Z(),
	})

	// ADR 0061 — wire the WebAuthn lib instance when env is configured.
	// Nil falls through to ErrWebAuthnNotImplemented in the handlers,
	// so a deploy that hasn't set SEDOC_WEBAUTHN_RPID gets a clean
	// 501 rather than a 5xx panic.
	if waCfg := service.LoadWebAuthnConfigFromEnv(); waCfg != nil {
		wa, err := service.NewWebAuthnLib(waCfg)
		if err != nil {
			log.Warn(ctx).Err(err).Msg("webauthn lib init failed; passkey routes will 501")
		} else {
			svc.WebAuthnLib = wa
			log.Info(ctx).Str("rpid", waCfg.RPID).Msg("webauthn passkey support enabled")
		}
	} else {
		log.Info(ctx).Msg("webauthn not configured (SEDOC_WEBAUTHN_RPID unset); passkey routes will 501")
	}

	// ---- MFA complete surface (ADR 0063) ---------------------------------
	// Always wire the deps; nil-valued fields degrade to "method
	// unavailable" without erroring. Stub mode kicks in when the
	// platform credentials aren't configured — useful for dev.
	stubLog := func(dest, code string) {
		log.Info(ctx).Str("dest", dest).Str("code", code).Msg("mfa otp (stub mode)")
	}
	smsSender := notifications.NewSMSSender(notifications.SMSConfig{
		AccountSID:       os.Getenv("SEDOC_TWILIO_ACCOUNT_SID"),
		AuthToken:        os.Getenv("SEDOC_TWILIO_AUTH_TOKEN"),
		VerifyServiceSID: os.Getenv("SEDOC_TWILIO_VERIFY_SID"),
		DevStub:          os.Getenv("SEDOC_TWILIO_ACCOUNT_SID") == "",
		StubLog:          stubLog,
	})
	emailSender := notifications.NewEmailOTPSender(notifications.EmailOTPConfig{
		Host:          os.Getenv("SEDOC_SMTP_HOST"),
		Port:          587,
		Username:      os.Getenv("SEDOC_SMTP_USER"),
		Password:      os.Getenv("SEDOC_SMTP_PASSWORD"),
		From:          os.Getenv("SEDOC_SMTP_FROM"),
		SubjectPrefix: "[SeDoc]",
		DevStub:       os.Getenv("SEDOC_SMTP_HOST") == "",
		StubLog:       stubLog,
	})
	svc.SetMFADeps(service.MFADeps{
		SMS:   smsSender,
		Email: emailSender,
		// SEDOC_PUSH_SENDER selects the dispatcher: nats (default, emits
		// dms.auth.mfa_push_challenged.v1 for downstream FCM/APNs delivery),
		// log (dev/observability), or noop. Direct FCM/APNs lands with the
		// mobile app (Phase 11.3).
		Push: notifications.NewSender(os.Getenv("SEDOC_PUSH_SENDER"), nc, *log.Z()),
	})

	// Per-tenant Twilio + SMTP credentials (migration 000044). When a
	// tenant has saved their own creds via the admin UI, MFA senders
	// are built fresh per-call from these rows; env-mode senders above
	// remain the deployment-wide fallback. The SMTP-password seal key
	// MUST match the notification service's derive function (same
	// domain prefix) so password_sealed unseals from either side.
	svc.SetNotifRepo(repository.New(pool))
	svc.SetNotifSealKey(deriveSMTPSealKey(cfg.LocalKEK))

	// ---- LDAP / AD direct bind (ADR 0062) --------------------------------
	// Always wire the repo + pool; tenants without an active config
	// row stay on the local-password path. The pool is per-process.
	ldapRepo := repository.NewLDAPRepo()
	ldapPool := ldap.NewPool(ldap.DefaultPool())
	defer ldapPool.CloseAll()
	svc.SetLDAP(service.LDAPDeps{Repo: ldapRepo, Pool: ldapPool})

	// 15-min sync ticker with ±60s jitter so multiple instances of
	// the auth service don't hammer the same directory at the top
	// of every quarter-hour.
	go func() {
		jitter := time.Duration(rand.Int63n(int64(60 * time.Second))) //nolint:gosec // non-crypto jitter
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter):
		}
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		// Run once on boot so admins don't wait 15 min for the first
		// data after enabling sync.
		svc.SyncAllTenants(ctx, pool)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				svc.SyncAllTenants(ctx, pool)
			}
		}
	}()

	// ---- Handler ----------------------------------------------------------
	cookieSecure := cfg.Environment == "prod" || cfg.Environment == "staging"
	h := handler.New(handler.Config{
		Service:      svc,
		Logger:       *log.Z(),
		CookieSecure: cookieSecure,
		// ERP integration Files BFF target for /api/v1/integrations/erp/*.
		// Defaults to http://localhost:8091 when unset.
		IntegrationBFFURL: os.Getenv("SEDOC_INTEGRATION_BFF_URL"),
	})

	// ---- SAML 2.0 (Phase A2) ----------------------------------------------
	// Durable SP identity: operator-injected PEMs (K8s secret / Vault) if
	// present, else the app-managed keypair persisted in saml_sp_keypair
	// — generated once on first boot and reused across restarts, so the
	// SP cert an IdP pins stays STABLE (fixes the every-boot regenerate
	// that caused intermittent SSO outages). Logs the cert fingerprint.
	spKM, err := sso.ResolveSPKeyMaterial(ctx, pool, localKEK,
		os.Getenv("SEDOC_SAML_SP_KEY_PEM"), os.Getenv("SEDOC_SAML_SP_CERT_PEM"), *log.Z())
	if err != nil {
		log.Warn(ctx).Err(err).Msg("saml sp init failed; SSO routes will be disabled")
		spKM = nil
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
	hs := health.NewServerWithMeta("auth", cfg.Region, pool, rdb, nc, nil)
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
				}, handler.NewGroupsHandler(pool, *log.Z(), cfg.Environment),
					handler.NewSSOAdminHandler(pool, *log.Z()),
					handler.NewSCIMAdminHandler(pool, *log.Z()),
					handler.NewEncryptionAdminHandler(pool, *log.Z()),
					handler.NewTenantAdminHandler(pool, *log.Z()),
					handler.NewLDAPAdminHandler(pool, svc, ldapRepo, *log.Z()))),
			),
		),
	)

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           middleware.RequireGatewaySignature()(rootMux),
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
		return nil, fmt.Errorf("SEDOC_LOCAL_KEK not set")
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

// deriveSMTPSealKey MUST match services/notification/cmd/server/main.go's
// function of the same name — both services seal/unseal the same
// tenant_smtp_configs.password_sealed column.
func deriveSMTPSealKey(kek string) []byte {
	h := sha256.Sum256([]byte("vaultdms.notification.smtp.seal.v1:" + kek))
	return h[:]
}
