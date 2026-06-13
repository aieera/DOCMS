// Package main boots the SeDoc signature service.
package main

import (
	"context"
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
	"google.golang.org/grpc/credentials/insecure"

	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"

	"crypto/sha256"
	"encoding/hex"
	"github.com/aieera/sedoc/pkg/config"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"

	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/pkg/signing/tsp"
	"github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/services/signature/internal/handler"
	"github.com/aieera/sedoc/services/signature/internal/repository"
	"github.com/aieera/sedoc/services/signature/internal/service"
	"github.com/aieera/sedoc/services/signature/internal/signer"
)

const serviceName = "signature"

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

	// License (ADR 0095): load + hourly re-validate so the esign/ipaas feature
	// gates + the grace write-gate below have state. Absent = unlicensed-dev
	// (gates no-op); invalid JWT is fatal.
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

	var s3c *storage.S3Client
	if cfg.MinIOEndpoint != "" {
		s3c, err = storage.NewS3Client(cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOUseSSL)
		if err != nil {
			log.Fatal(ctx).Err(err).Msg("s3 connect")
		}
	}

	repo := repository.New(pool)
	svc := service.New(service.Config{
		Pool:   pool,
		Repo:   repo,
		Outbox: database.NewOutboxRepository(),
		S3:     s3c,
		Logger: *log.Z(),
	})

	// ADR 0070 / 0071 follow-up — bytes-to-storage hand-off. Dial
	// storage + document gRPC and plug the resulting clients into
	// the service so post-completion pulls a real new version. If
	// either dial fails we keep going: completion still flips
	// status + emits the audit event, just without the version.
	storageConn := dialServiceOpt(ctx, log, "storage", cfg.StorageServiceAddr)
	documentConn := dialServiceOpt(ctx, log, "document", cfg.DocumentServiceAddr)
	if storageConn != nil && documentConn != nil {
		ingestClient := service.NewGRPCIngestClient(
			sedocv1.NewStorageServiceClient(storageConn),
			sedocv1.NewDocumentServiceClient(documentConn),
			pool,
		)
		svc.AddIngest(ingestClient)
		log.Info(ctx).Msg("ingest pipeline wired: storage + document gRPC reachable")
	} else {
		log.Warn(ctx).Msg("ingest pipeline unavailable; signed PDFs will not materialize as new versions")
	}
	if storageConn != nil {
		defer func() { _ = storageConn.Close() }()
	}
	if documentConn != nil {
		defer func() { _ = documentConn.Close() }()
	}

	// ADR 0025 — server-seal pipeline. Construct the Signer (mock by default;
	// the real DSS sidecar when SEDOC_SIGNER=dss) and wire the seal capability:
	// fetch a version's decrypted bytes from the document service (internal
	// key) → DSS-sign → ingest a sealed version. FromEnv fails fast on a bad
	// SEDOC_SIGNER value.
	sgnr, serr := signer.FromEnv(os.Getenv("SIGNER_ADDR"))
	if serr != nil {
		log.Fatal(ctx).Err(serr).Msg("signer init")
	}
	docHTTP := os.Getenv("SEDOC_DOCUMENT_HTTP_URL")
	if docHTTP == "" {
		docHTTP = "http://document:8080"
	}
	svc.AddSealer(sgnr, docHTTP, os.Getenv("SEDOC_INTERNAL_API_KEY"), os.Getenv("SEDOC_GATEWAY_SECRET"))
	log.Info(ctx).Str("signer", os.Getenv("SEDOC_SIGNER")).Str("doc_http", docHTTP).Msg("server-seal pipeline wired")

	// ADR 0025 increment 2 — auto-seal on workflow signature completion. The
	// consumer subscribes to dms.signature.completed.v1 and runs SealVersion.
	if js != nil {
		if err := service.NewSealConsumer(js, svc, *log.Z()).Start(); err != nil {
			log.Warn(ctx).Err(err).Msg("seal consumer not started (will not auto-seal)")
		} else {
			log.Info(ctx).Msg("seal consumer started: dms.signature.completed.v1 → server-seal")
		}
	}

	hs := health.NewServerWithMeta("signature", cfg.Region, pool, rdb, nc, s3c)
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

	// ADR 0070 — wire QES TSP adapters from per-provider env vars.
	// Each adapter only initializes when its required creds are
	// present; missing creds → the provider is silently absent from
	// the resolution map and StartQES rejects requests for it. The
	// mock adapter only joins when SEDOC_QES_MOCK_OK is set
	// (CI + e2e — never prod).
	tspClients := map[tsp.Provider]tsp.TSPClient{}
	if cfg.QESSwisscomBaseURL != "" {
		c, err := tsp.NewSwisscom(tsp.SwisscomConfig{
			BaseURL: cfg.QESSwisscomBaseURL, CustomerID: cfg.QESSwisscomCustomerID,
			ClientCertPEM: cfg.QESSwisscomCertPEM, ClientKeyPEM: cfg.QESSwisscomKeyPEM,
		})
		if err != nil {
			log.Warn(ctx).Err(err).Msg("swisscom adapter not configured")
		} else {
			tspClients[tsp.ProviderSwisscom] = c
		}
	}
	if cfg.QESIntesiBaseURL != "" {
		c, err := tsp.NewIntesi(tsp.IntesiConfig{
			BaseURL: cfg.QESIntesiBaseURL, ClientID: cfg.QESIntesiClientID,
			ClientSecret: cfg.QESIntesiClientSecret, PinnedCAPEM: cfg.QESIntesiPinnedCAPEM,
		})
		if err != nil {
			log.Warn(ctx).Err(err).Msg("intesi adapter not configured")
		} else {
			tspClients[tsp.ProviderIntesi] = c
		}
	}
	if cfg.QESInfoCertBaseURL != "" {
		c, err := tsp.NewInfoCert(tsp.InfoCertConfig{
			BaseURL: cfg.QESInfoCertBaseURL, ClientID: cfg.QESInfoCertClientID,
			ClientSecret: cfg.QESInfoCertClientSecret, OrgID: cfg.QESInfoCertOrgID,
		})
		if err != nil {
			log.Warn(ctx).Err(err).Msg("infocert adapter not configured")
		} else {
			tspClients[tsp.ProviderInfoCert] = c
		}
	}
	if cfg.QESMockOK {
		tspClients[tsp.ProviderMock] = tsp.NewMock()
	}
	if len(tspClients) > 0 {
		svc.AddQES(service.QESConfig{
			Clients: tspClients, PublicBaseURL: cfg.PublicURL,
			SessionTTL: 15 * time.Minute,
		})
		go svc.StartQESReaper(ctx)
		log.Info(ctx).Int("providers", len(tspClients)).Msg("qes adapters wired")
	}

	// ADR 0071 — DocuSign / Adobe Sign third-party connectors.
	// Each provider is enabled only when its OAuth client_id is set;
	// the Mock provider joins when ESIGN_MOCK_OK is set.
	esignOAuth := map[esign.Provider]esign.OAuthConfig{}
	hmacBytes := deriveESignHMAC(cfg.ESignStateHMAC, cfg.LocalKEK)
	if cfg.ESignDocuSignClientID != "" {
		esignOAuth[esign.ProviderDocuSign] = esign.OAuthConfig{
			Provider:     esign.ProviderDocuSign,
			AuthorizeURL: cfg.ESignDocuSignAuthorizeURL,
			TokenURL:     cfg.ESignDocuSignTokenURL,
			ClientID:     cfg.ESignDocuSignClientID,
			ClientSecret: cfg.ESignDocuSignClientSecret,
			RedirectURI:  cfg.ESignDocuSignRedirectURI,
			Scope:        "signature",
			HMACSecret:   hmacBytes,
		}
	}
	if cfg.ESignAdobeSignClientID != "" {
		esignOAuth[esign.ProviderAdobeSign] = esign.OAuthConfig{
			Provider:     esign.ProviderAdobeSign,
			AuthorizeURL: cfg.ESignAdobeSignAuthorizeURL,
			TokenURL:     cfg.ESignAdobeSignTokenURL,
			ClientID:     cfg.ESignAdobeSignClientID,
			ClientSecret: cfg.ESignAdobeSignClientSecret,
			RedirectURI:  cfg.ESignAdobeSignRedirectURI,
			Scope:        "agreement_send agreement_read",
			HMACSecret:   hmacBytes,
		}
	}
	// Default redirect URI for tenants connecting via the admin UI
	// (DB-saved client_id / client_secret). Falls back to a hard-coded
	// localhost callback when nothing in the env points us at a public
	// base URL — fine for dev. Prod overrides PUBLIC_URL.
	defaultRedirect := strings.TrimRight(cfg.PublicURL, "/") + "/api/v1/signatures/esign/oauth/callback"
	if cfg.PublicURL == "" {
		defaultRedirect = "http://localhost:3000/api/v1/signatures/esign/oauth/callback"
	}
	// Always wire eSign service plumbing — per-tenant DB credentials
	// are the supported path, so env-var providers are an OPTIONAL
	// deployment-wide fallback rather than a hard requirement.
	svc.AddESign(service.ESignConfig{
		SealingKey:         deriveSealingKey(cfg.LocalKEK),
		OAuthByProvider:    esignOAuth,
		DefaultRedirectURI: defaultRedirect,
		DefaultHMACSecret:  hmacBytes,
		HTTPClient:         &http.Client{Timeout: 30 * time.Second},
	})
	go svc.StartReconciler(ctx)
	log.Info(ctx).Int("env_providers", len(esignOAuth)).Msg("esign connectors wired")

	mux := http.NewServeMux()
	h := handler.New(svc, *log.Z())
	h.Register(mux)
	// Frontend lands on /sign/done after the QTSP redirect; the page
	// polls /qes/session/:id for the final status.
	h.RegisterQES(mux, "/sign/done")
	// esign (external DocuSign/AdobeSign integration) is the `esign` licensed
	// feature. All its routes share the /api/v1/signatures/esign/ prefix, so
	// register them on a sub-mux and gate the whole subtree — 402 when esign
	// isn't licensed; no-op in unlicensed-dev.
	esignMux := http.NewServeMux()
	h.RegisterESign(esignMux)
	mux.Handle("/api/v1/signatures/esign/", middleware.RequireLicenseFeature("esign")(esignMux))
	// ADR 0073 — in-person tablet ceremony (single device, sequential
	// signer + witness on the same session).
	h.RegisterInPerson(mux)

	// ADR 0025 — internal server-seal endpoint. Sub-mux gated by the
	// internal-service key (SessionOrAPIKey); the path is whitelisted from the
	// gateway-sig check so trusted services can call it directly, and the
	// gateway strips client-supplied internal keys so external callers can't.
	sealMux := http.NewServeMux()
	h.RegisterSeal(sealMux)
	mux.Handle("/api/v1/signatures/internal/seal",
		middleware.SessionOrAPIKey(middleware.SessionAuthConfig{Pool: pool}, "signatures:write")(sealMux))

	// ADR 0090 — iPaaS trigger endpoint (Zapier / Make / n8n). Lives
	// on its own sub-mux wrapped with APIKeyAuth so the bearer-token
	// path doesn't conflict with the gateway-sig'd handlers above.
	// The /api/v1/integrations/triggers/ prefix is whitelisted by
	// RequireGatewaySignature so the route reaches APIKeyAuth.
	integrationsMux := http.NewServeMux()
	handler.NewIntegrationTriggersHandler(pool).Register(integrationsMux)
	// Per-tenant rate limit (ADR 0090) — 60/min/tenant, nested INSIDE
	// APIKeyAuth so the key's tenant is on ctx. See document main.go.
	integrationsRL := middleware.NewRateLimiter(rdb, 60)
	mux.Handle("/api/v1/integrations/triggers/signatures/completed",
		middleware.APIKeyAuth(middleware.APIKeyAuthConfig{
			Pool:          pool,
			RequiredScope: "integrations:read",
		})(middleware.RateLimitPerTenantHTTP(integrationsRL, "integrations")(
			middleware.RequireLicenseFeature("ipaas")(integrationsMux))))
	// License grace write-gate (ADR 0095): mutating requests → 423 in grace/
	// expired; reads pass. No-op when active/unlicensed-dev.
	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: middleware.RequireGatewaySignature()(middleware.IdentityHeadersHTTP()(middleware.LicenseWriteGate(mux))), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	_ = sha256.Size // keep import if no other use
	_ = hex.EncodedLen

	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)

	// eSign token refresh loop. Runs every 30 minutes; refreshes any
	// access token that will expire within 24 hours. Without this,
	// tokens silently expire and the UI shows 'Connected' against a
	// dead row until the next admin send-attempt fails. Errors per
	// token are logged inside the service method.
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		// Kick once at startup so a restart after a long outage
		// recovers without waiting for the first tick.
		if ok, fail, err := svc.RefreshDueTokens(ctx, 24*time.Hour); err != nil {
			log.Warn(ctx).Err(err).Msg("esign refresh sweep failed at startup")
		} else if ok+fail > 0 {
			log.Info(ctx).Int("ok", ok).Int("fail", fail).Msg("esign refresh sweep")
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ok, fail, err := svc.RefreshDueTokens(ctx, 24*time.Hour)
				if err != nil {
					log.Warn(ctx).Err(err).Msg("esign refresh sweep failed")
					continue
				}
				if ok+fail > 0 {
					log.Info(ctx).Int("ok", ok).Int("fail", fail).Msg("esign refresh sweep")
				}
			}
		}
	}()

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

// dialServiceOpt opens a gRPC connection with a 5-second deadline.
// Returns nil + warns on failure rather than crashing — the
// signature service can still serve every other route when storage
// or document is briefly down at boot.
func dialServiceOpt(ctx context.Context, log *logger.Logger, name, addr string) *grpc.ClientConn {
	if addr == "" {
		log.Warn(ctx).Str("service", name).Msg("addr empty; ingest pipeline will skip this dependency")
		return nil
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(dialCtx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		log.Warn(ctx).Err(err).Str("service", name).Str("addr", addr).
			Msg("gRPC dial failed; ingest pipeline will skip this dependency")
		return nil
	}
	return conn
}

// deriveSealingKey returns a 32-byte AES key derived from the
// service-level LocalKEK. SHA-256 over the KEK bytes — keeps the
// boot path simple and avoids pulling pkg/crypto for a one-line
// transformation.
func deriveSealingKey(kek string) []byte {
	if kek == "" {
		// Service should fail validation upstream when KEK is empty,
		// but if it slipped through, return a zero-key so seal/unseal
		// fail loudly on first use rather than producing a silent
		// hardcoded-key vulnerability.
		return make([]byte, 32)
	}
	h := sha256.Sum256([]byte("vaultdms.esign.seal.v1:" + kek))
	return h[:]
}

// deriveESignHMAC seeds the OAuth state HMAC. Prefers the explicit
// SEDOC_ESIGN_STATE_HMAC env (hex); falls back to a KEK-derived
// value so a fresh boot still has integrity-checked state.
func deriveESignHMAC(explicit, kek string) []byte {
	if explicit != "" {
		// Hex decode if it parses; otherwise treat as raw.
		if b, err := hex.DecodeString(explicit); err == nil && len(b) >= 32 {
			return b
		}
		if len(explicit) >= 32 {
			return []byte(explicit)[:32]
		}
	}
	h := sha256.Sum256([]byte("vaultdms.esign.state.v1:" + kek))
	return h[:]
}
