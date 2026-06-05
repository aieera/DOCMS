// Package main boots the SeDoc document service — the Phase-5 reference
// implementation. It wires repositories, service, Policy gRPC client, and
// handler onto a gRPC server, a grpc-gateway REST mux, a dedicated health
// server, and the transactional outbox publisher.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/redis/go-redis/v9"
	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/config"
	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/pkg/storage"

	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/compliance"
	"github.com/aieera/sedoc/services/document/internal/bulk"
	"github.com/aieera/sedoc/services/document/internal/handler"
	"github.com/aieera/sedoc/services/document/internal/janitor"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

const serviceName = "document"

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
	// public key bundled in pkg/license/dev_pubkey.go, and caches the
	// parsed claims for license.Current() readers. Absent license is OK
	// (unlicensed_dev_mode); only a *present but invalid* license is fatal,
	// unless SEDOC_REQUIRE_LICENSE=true escalates absence to fatal too.
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
	// FIX-7: refuse to boot under an unexpected RLS posture. Prod
	// connects as dms_app (NOBYPASSRLS); dev sets
	// SEDOC_ALLOW_BYPASS_RLS=1 to opt into the superuser
	// connection. Either drift produces a clear startup error rather
	// than the silent zero-row Inserts that motivated the audit.

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
			log.Fatal(ctx).Err(err).Msg("minio connect")
		}
	}

	// ---- Policy gRPC client ------------------------------------------------
	// Dev wiring uses insecure credentials and a static address. In production
	// this is swapped for mTLS via pkg/middleware and a service discovery step.
	policyConn, err := grpc.DialContext(ctx, cfg.PolicyServiceAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(5*time.Second),
	)
	if err != nil {
		log.Warn(ctx).Err(err).Str("addr", cfg.PolicyServiceAddr).
			Msg("policy service unreachable at startup; service will deny on checks until reachable")
	}
	var policyClient sedocv1.PolicyServiceClient
	if policyConn != nil {
		policyClient = sedocv1.NewPolicyServiceClient(policyConn)
		defer func() { _ = policyConn.Close() }()
	} else {
		policyClient = denyAllPolicyClient{}
	}

	// ---- Storage gRPC client (for the REST upload/download proxy) ---------
	// The storage service is gRPC-only; this service owns the browser-facing
	// REST surface and proxies to storage over gRPC. If storage is down at
	// boot, the proxy will surface 503s — same pattern as the policy client.
	storageConn, err := grpc.DialContext(ctx, cfg.StorageServiceAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(5*time.Second),
	)
	if err != nil {
		log.Warn(ctx).Err(err).Str("addr", cfg.StorageServiceAddr).
			Msg("storage service unreachable at startup; proxy will 503 until reachable")
	}
	var storageClient sedocv1.StorageServiceClient
	if storageConn != nil {
		storageClient = sedocv1.NewStorageServiceClient(storageConn)
		defer func() { _ = storageConn.Close() }()
	}

	// ---- Wire repos → service → handler -----------------------------------
	repos := repository.New(pool)
	holdsService := compliance.NewHoldsService(pool)
	svc := service.New(pool, repos, policyClient, *log.Z())
	svc.SetHoldsChecker(holdsService)
	svc.SetEnvironment(cfg.Environment)
	// ADR 0078 — base64-decode SEDOC_LOCAL_KEK so the NER api-key
	// Set/Clear endpoints can encrypt with AES-256-GCM. Same key the
	// auth service uses for MFA secrets and the intelligence worker
	// uses to decrypt the per-tenant LLM key. Empty / wrong-size KEK
	// leaves the path disabled — Set returns 500 with a clear error.
	if kekB64 := os.Getenv("SEDOC_LOCAL_KEK"); kekB64 != "" {
		if kek, err := base64.StdEncoding.DecodeString(kekB64); err == nil {
			svc.SetLocalKEK(kek)
		} else {
			log.Warn(ctx).Err(err).Msg("SEDOC_LOCAL_KEK base64 decode failed; tenant secrets disabled")
		}
	}
	// Admin Trash purge needs the S3 client to delete blob bytes
	// alongside the DB rows. Nil-safe — when MinIO isn't wired,
	// PurgeDocument fails with a clear "s3 not configured" error.
	if s3c != nil {
		svc.SetS3Client(s3c)
	}
	// LocalKeyManager — same KEK used by storage's encrypt-at-rest path.
	// The decrypt-stream handler unwraps per-blob DEKs through this so
	// downloads of envelope-encrypted blobs return plaintext. Nil leaves
	// decrypt-stream working only for unencrypted blobs.
	var docKMS pkgcrypto.KeyManager
	if kekB64 := os.Getenv("SEDOC_LOCAL_KEK"); kekB64 != "" {
		if lkm, kerr := pkgcrypto.NewLocalKeyManager(kekB64, func(msg string) {
			log.Warn(ctx).Msg(msg)
		}); kerr != nil {
			log.Warn(ctx).Err(kerr).Msg("local kek invalid; decrypt-stream falls back to passthrough only")
		} else {
			docKMS = lkm
		}
	}
	docHandler := handler.New(svc, *log.Z(), cfg.PublicURL)
	holdsHandler := handler.NewHoldsHandler(holdsService, *log.Z())
	// ADR 0061 — gate /compliance/holds/{id}/release behind a
	// 5-min fresh-passkey grant. Pool is the same one the rest of
	// the service uses; nil disables the gate (dev deploys without
	// WebAuthn configured).
	holdsHandler.SetStepUpPool(pool)

	// Temporal client for DSR workflow dispatch (Wave 8.3). Best-effort:
	// if Temporal is unreachable at boot the handler surface still
	// works; rows sit in privacy_dsr_requests.status='pending' until an
	// operator re-dispatches.
	var tcDSR temporalclient.Client
	if cfg.TemporalAddr != "" {
		if c, terr := temporalclient.Dial(temporalclient.Options{HostPort: cfg.TemporalAddr, Namespace: "vaultdms"}); terr == nil {
			tcDSR = c
			defer c.Close()
		} else {
			log.Warn(ctx).Err(terr).Str("addr", cfg.TemporalAddr).
				Msg("temporal unreachable; DSR requests will queue as pending")
		}
	}
	dsrSalt := os.Getenv("SEDOC_DSR_ANONYMIZE_SALT")
	if dsrSalt == "" {
		// Deterministic per-tenant-deployment fallback so anonymize still
		// works in dev. Prod must set the env var.
		dsrSalt = "vaultdms-dev-salt"
	}
	privacyHandler := handler.NewPrivacyHandler(pool, tcDSR, dsrSalt, *log.Z())
	dsrVerifyHandler := handler.NewDSRVerifyHandler(pool, rdb, *log.Z())
	redactionHandler := handler.NewRedactionHandler(pool, holdsService, *log.Z())
	residencyHandler := handler.NewResidencyHandler(pool, tcDSR, *log.Z())
	shareLinksAdminHandler := handler.NewShareLinksAdminHandler(svc, *log.Z())
	retentionPolicyHandler := handler.NewRetentionPolicyHandler(pool, svc, *log.Z())

	// FIX-2 (2026-05-31): both download surfaces now take *svc so they
	// can call EnsureCanViewDocument before producing presigned URLs
	// (storageProxy) or streaming decrypted bytes (decryptStreamHandler).
	storageProxy := handler.NewStorageProxy(storageClient, pool, svc)
	decryptStreamHandler := handler.NewDecryptStreamHandler(pool, s3c, docKMS, svc, *log.Z())

	// ---- Health ------------------------------------------------------------
	hs := health.NewServerWithMeta("document", cfg.Region, pool, rdb, nc, s3c)
	go func() {
		addr := fmt.Sprintf(":%d", cfg.HealthPort)
		if err := hs.Start(addr); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	// ---- gRPC --------------------------------------------------------------
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(
		middleware.RecoveryInterceptor(log),
		middleware.CorrelationInterceptor(),
		middleware.TenantInterceptor(pool),
		// CLAUDE.md: missing this interceptor causes auth.GetUserID/Role
		// to return zero values inside handlers, which makes OPA Rule 5
		// (workspace admin) and Rule 6 (owner/admin) silently deny —
		// surfacing as PermissionDenied on every authenticated read.
		middleware.UserIdentityInterceptor(),
		middleware.RequestLogInterceptor(log),
	))
	sedocv1.RegisterDocumentServiceServer(grpcSrv, docHandler)

	// ADR 0075 — bulk import + export. Lives in the document service
	// because workspaces, folders, and documents are document-owned.
	// User + group bulk dispatches outbound to auth via the auth gRPC
	// client (nil-safe — when authConn is nil those rows return
	// "auth service not configured" without aborting the batch).
	var authClient sedocv1.AuthServiceClient
	if authConn := dialAuthOpt(ctx, log, cfg.AuthServiceAddr); authConn != nil {
		authClient = sedocv1.NewAuthServiceClient(authConn)
		defer func() { _ = authConn.Close() }()
	}
	bulkRepo := bulk.NewRepo(pool)
	bulkSvc := bulk.NewService(pool, repos, bulkRepo, authClient, *log.Z())
	sedocv1.RegisterBulkServiceServer(grpcSrv, bulk.NewGRPCServer(bulkSvc))
	bulkHTTP := bulk.NewHTTPHandler(bulkSvc, *log.Z())

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

	// ---- HTTP (grpc-gateway proxies REST → gRPC on localhost) --------------
	// grpc-gateway's default header matcher only forwards
	// Grpc-Metadata-* headers into gRPC metadata. The auth/tenant
	// interceptors look at x-tenant-id / x-user-id metadata keys
	// which come from the upstream X-Tenant-ID / X-User-ID HTTP
	// headers (injected by the gateway in prod, by Vite in host dev).
	// Whitelist them explicitly or every request arrives with empty
	// tenant context → 401.
	// UseProtoNames keeps JSON keys in snake_case (workspace_id) to
	// match the frontend type definitions and the rest of the REST
	// surface. Without this, grpc-gateway defaults to lowerCamelCase
	// (workspaceId) and the frontend's doc.workspace_id reads as
	// undefined — URLs like /workspaces/undefined/documents/... were
	// the visible symptom.
	gwMux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
			MarshalOptions: protojson.MarshalOptions{
				UseProtoNames:   true,
				EmitUnpopulated: true,
			},
			UnmarshalOptions: protojson.UnmarshalOptions{
				DiscardUnknown: true,
			},
		}),
	)

	// grpcGatewayInject wraps gwMux so X-Tenant-ID / X-User-ID / role
	// HTTP headers land in gRPC metadata. runtime.WithMetadata /
	// WithIncomingHeaderMatcher both rely on grpc-gateway's own
	// propagation which was lossy in practice; this middleware uses
	// metadata.AppendToOutgoingContext directly which grpc-gateway
	// forwards verbatim to the gRPC call.
	// grpc-gateway forwards any `Grpc-Metadata-*` request header as
	// matching gRPC metadata verbatim (prefix stripped, key lowercased).
	// Rewriting inbound headers is the most reliable path — it
	// doesn't depend on WithMetadata / WithIncomingHeaderMatcher, both
	// of which proved lossy in host-dev mode.
	grpcGatewayInject := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			get := func(k string) string { return r.Header.Get(k) }
			tid := get("X-Tenant-ID")
			if tid == "" {
				tid = get("X-Auth-Tenant-ID")
			}
			// Cookie-only browser callers (the React app) reach this
			// middleware *after* SessionAuthOptional has populated
			// auth.UserInfo on ctx but *without* X-User-*/X-Tenant-ID
			// HTTP headers. The previous header-only read produced
			// gRPC-side ctx with no user_role, so OPA's Rule 6
			// (org admin/owner) never fired and the list endpoints
			// 403'd unless the user was an explicit workspace_member.
			// Fall back to ctx so SessionAuth is the single source.
			user, userErr := auth.User(r.Context())
			if tid == "" {
				if existing, e := auth.GetTenantID(r.Context()); e == nil && existing != uuid.Nil {
					tid = existing.String()
				}
			}
			if tid != "" {
				r.Header.Set("Grpc-Metadata-X-Tenant-Id", tid)
			}
			if v := get("X-User-ID"); v != "" {
				r.Header.Set("Grpc-Metadata-X-User-Id", v)
			} else if userErr == nil && user.ID != uuid.Nil {
				r.Header.Set("Grpc-Metadata-X-User-Id", user.ID.String())
			}
			if v := get("X-User-Role"); v != "" {
				r.Header.Set("Grpc-Metadata-X-User-Role", v)
			} else if userErr == nil && user.Role != "" {
				r.Header.Set("Grpc-Metadata-X-User-Role", user.Role)
			}
			if userErr == nil && user.Email != "" {
				r.Header.Set("Grpc-Metadata-X-User-Name", user.Email)
			}
			next.ServeHTTP(w, r)
		})
	}
	gwConn, err := grpc.DialContext(ctx, fmt.Sprintf("localhost:%d", cfg.GRPCPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(5*time.Second),
	)
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("gateway dial")
	}
	defer func() { _ = gwConn.Close() }()
	if err := sedocv1.RegisterDocumentServiceHandler(ctx, gwMux, gwConn); err != nil {
		log.Fatal(ctx).Err(err).Msg("gateway register")
	}

	// Rate limit only /api/v1/shared/{token} — public, anonymous endpoint.
	// Spec is 10 req/min per IP; burst matches steady rate so a quick
	// click-through (peek + verify-password = 2 calls) doesn't trip the
	// limiter on the first interaction.
	shareLimiter := middleware.NewIPRateLimiter(10, 10, time.Minute)

	// Custom mux to wrap only the share endpoint
	rootMux := http.NewServeMux()
	// FIX-10 follow-up: the anonymous bytes endpoint sits in its own
	// mux and binds the EXACT path so Go 1.22's ServeMux gives it
	// priority over the /api/v1/shared/ prefix below (longer pattern
	// wins). Same rate-limiter wraps it so the 10 req/min/IP budget
	// covers downloads too.
	shareDownloadMux := http.NewServeMux()
	handler.NewShareDownloadHandler(pool, s3c, docKMS, svc, *log.Z()).Register(shareDownloadMux)
	rootMux.Handle("GET /api/v1/shared/{token}/download", shareLimiter(shareDownloadMux))
	rootMux.Handle("/api/v1/shared/", shareLimiter(gwMux))

	// Storage REST proxy — forwards to the storage gRPC service. The
	// proxy reads X-Tenant-ID / X-User-ID from the inbound HTTP request
	// (same pattern as the grpc-gateway) and copies them onto outbound
	// gRPC metadata.
	storageMux := http.NewServeMux()
	storageProxy.Register(storageMux)
	// The same handler is exposed at a second URL shape (see proxy.RegisterDownloadAlias
	// godoc) so browser-native loaders like `<img src=".../versions/{vid}/download">`
	// find the route they expect. Lives on its own mux because the path
	// doesn't share the /api/v1/storage/ prefix.
	downloadAliasMux := http.NewServeMux()
	storageProxy.RegisterDownloadAlias(downloadAliasMux)
	// SessionAuth populates auth.UserInfo on ctx from the dms_session
	// cookie; the proxy's outbound() reads tenant + user from ctx and
	// injects them into outbound gRPC metadata. Without this the
	// upstream Kong path wasn't in play (Vite host-mode proxy bypasses
	// Kong) so X-Tenant-ID never reached the proxy and InitiateUpload
	// failed with INVALID_ARGUMENT before doing any work.
	// ERP outbound push accepts a Bearer API key (scope "upload") here in
	// addition to the session cookie, so the sync worker can drive the
	// initiate → PUT → complete flow service-to-service. SessionOrAPIKey
	// stamps the same tenant/user identity either way, which the proxy's
	// outbound() injects into the storage gRPC metadata.
	rootMux.Handle("/api/v1/storage/", middleware.CorrelationHTTP(
		middleware.SessionOrAPIKey(middleware.SessionAuthConfig{Pool: pool}, "upload")(storageMux),
	))
	// Download alias — handled by storageProxy.download but addressed at
	// /api/v1/documents/{id}/versions/{vid}/download for backward compat
	// with viewers + OnlyOffice + redaction_review. Same SessionAuth chain
	// so the cookie resolves the user/tenant; the proxy then injects them
	// into outbound gRPC metadata.
	rootMux.Handle("GET /api/v1/documents/{document_id}/versions/{version_id}/download",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(downloadAliasMux),
		))
	// Decrypt-stream — for envelope-encrypted blobs, the existing
	// presigned MinIO URL streams raw ciphertext that browser viewers
	// can't parse. This route reads the encrypted_dek metadata, unwraps
	// via the tenant KEK, AES-GCM decrypts, and streams plaintext.
	// Unencrypted blobs pass through unchanged so callers can use one
	// URL regardless of encryption state.
	decryptStreamMux := http.NewServeMux()
	decryptStreamHandler.Register(decryptStreamMux)
	rootMux.Handle("GET /api/v1/documents/{document_id}/versions/{version_id}/decrypt-stream",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(decryptStreamMux),
		))

	// ADR 0090 — iPaaS trigger endpoints (Zapier / Make / n8n).
	// Authenticated by API key (Bearer vdms_...) with scope
	// integrations:read; tenant is stamped on ctx by APIKeyAuth.
	integrationsMux := http.NewServeMux()
	handler.NewIntegrationTriggersHandler(pool).Register(integrationsMux)
	// Per-tenant rate limit (ADR 0090). 60/min/tenant is generous for poll
	// triggers (Zapier polls every 1-15 min) but caps abusive polling PER
	// TENANT — not per-IP, so distinct keys behind one NAT can't pool a
	// budget. Overridable via redis ratelimit:config:{tenant}:integrations.
	// Nested INSIDE APIKeyAuth so the tenant the key resolved to is on ctx.
	integrationsRL := middleware.NewRateLimiter(rdb, 60)
	// License gate (ADR 0095): iPaaS/integration triggers are the `ipaas`
	// licensed feature. 402 when not licensed; no-op in unlicensed-dev.
	// Innermost so auth + rate-limit establish the tenant first.
	rootMux.Handle("/api/v1/integrations/triggers/documents", middleware.CorrelationHTTP(
		middleware.APIKeyAuth(middleware.APIKeyAuthConfig{
			Pool:          pool,
			RequiredScope: "integrations:read",
		})(middleware.RateLimitPerTenantHTTP(integrationsRL, "integrations")(
			middleware.RequireLicenseFeature("ipaas")(integrationsMux))),
	))

	// ADR 0112 — Outlook add-in ingest. Uses session auth (not API
	// key) because the add-in establishes a SeDoc session via
	// the /auth/m365/exchange endpoint and then attaches it as a
	// Bearer header on this route.
	m365IngestMux := http.NewServeMux()
	handler.NewM365IngestHandler(pool, svc).Register(m365IngestMux)
	// Per-IP rate limit so a compromised Outlook session can't spam
	// document creation and exhaust tenant storage quota. 60 req/min
	// is plenty for an honest user (the add-in only POSTs on explicit
	// "Save to SeDoc" click).
	m365IngestLimiter := middleware.NewIPRateLimiter(60, 60, time.Minute)
	rootMux.Handle("/api/v1/integrations/m365/", middleware.CorrelationHTTP(
		m365IngestLimiter(middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(m365IngestMux)),
	))

	// Compliance REST endpoints (legal holds — Wave 8.2). Uses its own
	// mux so 423 Locked + validation errors flow through the handler's
	// own writer rather than being rewrapped by the gateway.
	complianceMux := http.NewServeMux()
	holdsHandler.Register(complianceMux)
	rootMux.Handle("/api/v1/compliance/", middleware.CorrelationHTTP(complianceMux))

	// Privacy (GDPR DSR) REST endpoints — Wave 8.3.
	privacyMux := http.NewServeMux()
	privacyHandler.Register(privacyMux)
	dsrVerifyHandler.Register(privacyMux)
	rootMux.Handle("/api/v1/privacy/", middleware.CorrelationHTTP(privacyMux))

	// Residency dashboard + migrate-documents — Wave 8.4.
	residencyMux := http.NewServeMux()
	residencyHandler.Register(residencyMux)
	rootMux.Handle("/api/v1/residency/", middleware.CorrelationHTTP(residencyMux))

	// Share-link admin — Wave 10. Shares the /api/v1/admin/ prefix
	// with the auth service's /api/v1/admin/users + /api/v1/admin/groups,
	// but the document service owns share-links so it routes here.
	//
	// SessionAuth runs first so the handler's enrich() can read the
	// caller's role from auth.User(ctx). Without it, host-mode (no
	// Kong in front) admins hit a 403 because the policy check sees
	// an empty user_role and Rule 6 (owner allow) doesn't fire.
	shareLinksAdminMux := http.NewServeMux()
	shareLinksAdminHandler.Register(shareLinksAdminMux)
	shareLinksAdminWrapped := middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(shareLinksAdminMux),
	)
	rootMux.Handle("/api/v1/admin/share-links", shareLinksAdminWrapped)
	rootMux.Handle("/api/v1/admin/share-links/", shareLinksAdminWrapped)

	// ADR 0075 — bulk import + export HTTP facade. Same SessionAuth
	// chain as the other admin handlers so the calling user's role
	// reaches OPA via auth.User(ctx).
	bulkAdminMux := http.NewServeMux()
	bulkHTTP.Register(bulkAdminMux)
	bulkAdminWrapped := middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(bulkAdminMux),
	)
	rootMux.Handle("/api/v1/admin/bulk/", bulkAdminWrapped)
	rootMux.Handle("/api/v1/admin/documents/", shareLinksAdminWrapped)

	// Retention policies admin — Wave 10 + Phase 5 preview/exempt.
	retentionPolicyMux := http.NewServeMux()
	retentionPolicyHandler.Register(retentionPolicyMux)
	rootMux.Handle("/api/v1/admin/retention-policies", middleware.CorrelationHTTP(retentionPolicyMux))
	rootMux.Handle("/api/v1/admin/retention-policies/", middleware.CorrelationHTTP(retentionPolicyMux))

	// Phase 5 — per-document retention exemption (distinct from legal
	// hold; see service.SetDocumentRetentionExempt + migration 000054).
	retentionExemptMux := http.NewServeMux()
	handler.NewRetentionExemptHandler(svc).Register(retentionExemptMux)
	rootMux.Handle("POST /api/v1/documents/{id}/retention-exempt",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(retentionExemptMux),
		))

	// Shared-with-me — cross-workspace discovery of folders the
	// caller holds a grant for (direct or via group). SessionAuth
	// populates ctx; no role gate (any authenticated user can read
	// their own grants).
	sharedWithMeMux := http.NewServeMux()
	handler.NewSharedWithMeHandler(svc).Register(sharedWithMeMux)
	rootMux.Handle("GET /api/v1/folders/shared-with-me",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(sharedWithMeMux),
		))

	// Folder cascade-restore (FIX-5). DeleteFolder is the gRPC rpc
	// that triggers SoftDeleteSubtree; this side-mux exposes the
	// matching restore action without a proto regen. Service-layer
	// requirePermission ("admin" on the folder) gates internally.
	folderRestoreMux := http.NewServeMux()
	handler.NewFolderRestoreHandler(svc).Register(folderRestoreMux)
	rootMux.Handle("POST /api/v1/folders/{folder_id}/restore",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(folderRestoreMux),
		))

	// Workspace members — per-workspace ACL surface. Gating lives in
	// the service layer (creator OR tenant admin for writes; any
	// member for reads).
	workspaceMembersMux := http.NewServeMux()
	handler.NewWorkspaceMembersHandler(svc).Register(workspaceMembersMux)
	workspaceMembersAuth := middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(workspaceMembersMux)
	rootMux.Handle("GET /api/v1/workspaces/{workspace_id}/members", middleware.CorrelationHTTP(workspaceMembersAuth))
	rootMux.Handle("POST /api/v1/workspaces/{workspace_id}/members", middleware.CorrelationHTTP(workspaceMembersAuth))
	rootMux.Handle("PATCH /api/v1/workspaces/{workspace_id}/members/{user_id}", middleware.CorrelationHTTP(workspaceMembersAuth))
	rootMux.Handle("DELETE /api/v1/workspaces/{workspace_id}/members/{user_id}", middleware.CorrelationHTTP(workspaceMembersAuth))

	// Admin Trash — list soft-deleted docs + restore + permanent
	// purge (S3 + DB). All three routes role-gate to owner/admin
	// inside the handler. SessionAuth populates ctx so the service
	// layer's requireRole + tx wrapper see the right tenant/user.
	trashMux := http.NewServeMux()
	handler.NewTrashHandler(svc).Register(trashMux)
	trashAuth := middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(trashMux)
	rootMux.Handle("GET /api/v1/admin/trash", middleware.CorrelationHTTP(trashAuth))
	rootMux.Handle("GET /api/v1/admin/trash/folders", middleware.CorrelationHTTP(trashAuth))
	rootMux.Handle("POST /api/v1/admin/trash/{id}/restore", middleware.CorrelationHTTP(trashAuth))
	rootMux.Handle("DELETE /api/v1/admin/trash/{id}", middleware.CorrelationHTTP(trashAuth))

	// Compliance dashboard real-data feed. Replaces the mocked
	// docs-by-state / storage-by-region / encryption-coverage props
	// that previously rendered fictional numbers (94% / 67 GB
	// me-south-1) in /admin/compliance.
	complianceOverviewMux := http.NewServeMux()
	handler.NewComplianceOverviewHandler(pool).Register(complianceOverviewMux)
	rootMux.Handle("GET /api/v1/admin/compliance/overview", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(complianceOverviewMux),
	))

	// Phase 9 — named versions. PATCH the optional `label` on an existing
	// version row (migration 000055). Authorization mirrors the document
	// title-update rule ("edit" on the document).
	versionLabelMux := http.NewServeMux()
	handler.NewVersionLabelHandler(svc).Register(versionLabelMux)
	rootMux.Handle("PATCH /api/v1/documents/{document_id}/versions/{version_id}/label",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(versionLabelMux),
		))

	// Redaction endpoint — Wave 11.5. Uses Go 1.22 method+pattern
	// routing so only the /redact suffix lands here; everything else
	// under /api/v1/documents/ still flows to the grpc-gateway via
	// the root `/` handler below.
	redactionMux := http.NewServeMux()
	redactionHandler.Register(redactionMux)
	rootMux.Handle("POST /api/v1/documents/{id}/redact", middleware.CorrelationHTTP(redactionMux))

	// OCR results read API + re-OCR trigger. Owned by document
	// service because ocr_results lives in the document migration set;
	// intelligence service writes the rows, document serves them.
	ocrMux := http.NewServeMux()
	handler.NewOCRHandler(pool, *log.Z()).Register(ocrMux)
	rootMux.Handle("GET /api/v1/documents/{id}/versions/{vid}/ocr",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(ocrMux),
		))
	rootMux.Handle("POST /api/v1/documents/{id}/versions/{vid}/ocr/rerun",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(ocrMux),
		))

	// §9.5 / G9 — eDiscovery signed-ZIP export.
	ediscoveryMux := http.NewServeMux()
	handler.NewEDiscoveryHandler(svc, *log.Z()).Register(ediscoveryMux)
	rootMux.Handle("POST /api/v1/admin/ediscovery/export",
		middleware.CorrelationHTTP(ediscoveryMux))

	// ADR 0094 — admin DB-info surface (driver + version + feature
	// matrix). Read-only; future alt-driver work updates the matrix
	// without UI changes.
	dbInfoMux := http.NewServeMux()
	handler.NewDBInfoHandler(pool).Register(dbInfoMux)
	rootMux.Handle("/api/v1/admin/platform/db-info", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(dbInfoMux),
	))

	// Per-tenant upload format allowlist (migration 000060). Storage
	// service reads the same table on InitiateUpload; this surface is
	// the single source of truth admins write to. Owner/admin gated
	// inside the handler.
	uploadPolicyMux := http.NewServeMux()
	handler.NewUploadPolicyHandler(pool).Register(uploadPolicyMux)
	rootMux.Handle("/api/v1/admin/tenant/upload-policy", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(uploadPolicyMux),
	))

	// Phase 3 — workspace owner transfer. Outside the grpc-gateway
	// surface because it's an explicit owner-only action (not an
	// admin-capability rename) and benefits from a dedicated route
	// that the FE settings page can call without proto regen.
	wsTransferMux := http.NewServeMux()
	handler.NewWorkspaceTransferHandler(svc).Register(wsTransferMux)
	rootMux.Handle("POST /api/v1/workspaces/{workspace_id}/transfer-ownership",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(wsTransferMux),
		))

	// ADR 0095 — admin license-state surface (stub). Today returns
	// `unlicensed_dev_mode`; future JWT validator wires through here
	// without UI changes.
	licenseMux := http.NewServeMux()
	handler.NewLicenseHandler().Register(licenseMux)
	rootMux.Handle("/api/v1/admin/tenant/license", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(licenseMux),
	))

	// ADR 0098 — zero-trust view-only share. Admin routes require
	// SessionAuth; recipient routes (/api/v1/zt/{token}/*) are public
	// — the token IS the auth. Pepper is part of cfg so it can rotate
	// without rebuilding (env: SEDOC_ZT_TOKEN_PEPPER).
	ztPepper := []byte(os.Getenv("SEDOC_ZT_TOKEN_PEPPER"))
	if len(ztPepper) == 0 {
		ztPepper = []byte("dev-only-zt-pepper-rotate-in-prod")
	}
	ztHandler := handler.NewZTShareHandler(pool, storageProxy, ztPepper)

	ztAdminMux := http.NewServeMux()
	ztHandler.RegisterAdmin(ztAdminMux)
	rootMux.Handle("POST /api/v1/admin/share-links/zt", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(ztAdminMux),
	))
	rootMux.Handle("POST /api/v1/admin/share-links/zt/{token_id}/revoke",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(ztAdminMux),
		))
	rootMux.Handle("GET /api/v1/admin/share-links/zt/{token_id}/telemetry",
		middleware.CorrelationHTTP(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(ztAdminMux),
		))

	ztPublicMux := http.NewServeMux()
	ztHandler.RegisterPublic(ztPublicMux)
	// Per-IP rate limit on the public token-auth routes. 30 req/min
	// is comfortable for an honest viewer (page navigation + scroll
	// telemetry on a 50-page PDF) but caps brute-force token guessing
	// and telemetry spam from a single source. Matches the policy
	// already in place for /api/v1/shared/.
	ztPublicLimiter := middleware.NewIPRateLimiter(30, 30, time.Minute)
	rootMux.Handle("GET /api/v1/zt/{token_id}/manifest", middleware.CorrelationHTTP(ztPublicLimiter(ztPublicMux)))
	rootMux.Handle("GET /api/v1/zt/{token_id}/stream", middleware.CorrelationHTTP(ztPublicLimiter(ztPublicMux)))
	rootMux.Handle("POST /api/v1/zt/{token_id}/telemetry", middleware.CorrelationHTTP(ztPublicLimiter(ztPublicMux)))

	// ADR 0101 — cross-format compare. Single POST endpoint; no
	// new state. Reads canonical text from ocr_results; SessionAuth
	// sets tenant + user on ctx.
	compareMux := http.NewServeMux()
	handler.NewCompareHandler(pool).Register(compareMux)
	rootMux.Handle("POST /api/v1/compare", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(compareMux),
	))

	// ADR 0102 — predictive filing suggestions. Pre-upload predict +
	// post-decision feedback.
	pfMux := http.NewServeMux()
	handler.NewPredictiveFilingHandler(pool).Register(pfMux)
	rootMux.Handle("POST /api/v1/uploads/predict", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(pfMux),
	))
	rootMux.Handle("POST /api/v1/uploads/predict/feedback", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(pfMux),
	))

	// ADR 0104 — clause library CRUD + search. Five routes share one
	// SessionAuth chain; admin/owner role check is enforced inside
	// the mutating handlers via tenantOwnerOrFail.
	clMux := http.NewServeMux()
	handler.NewClausesHandler(pool).Register(clMux)
	clauseAuth := middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(clMux)
	rootMux.Handle("GET /api/v1/clauses",            middleware.CorrelationHTTP(clauseAuth))
	rootMux.Handle("GET /api/v1/clauses/{id}",       middleware.CorrelationHTTP(clauseAuth))
	rootMux.Handle("POST /api/v1/clauses",           middleware.CorrelationHTTP(clauseAuth))
	rootMux.Handle("PATCH /api/v1/clauses/{id}",     middleware.CorrelationHTTP(clauseAuth))
	rootMux.Handle("DELETE /api/v1/clauses/{id}",    middleware.CorrelationHTTP(clauseAuth))

	// ADR 0099 — contract intelligence graph. GET is read-only and
	// uses SessionAuth (so the tenant + user context is set); the
	// two mutating routes also require admin/owner role at the
	// handler layer.
	cgMux := http.NewServeMux()
	handler.NewContractGraphHandler(pool).Register(cgMux)
	rootMux.Handle("GET /api/v1/contracts/{document_id}/graph", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(cgMux),
	))
	rootMux.Handle("POST /api/v1/contracts/{document_id}/edges", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(cgMux),
	))
	rootMux.Handle("DELETE /api/v1/contracts/{document_id}/edges/{edge_id}", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(cgMux),
	))

	// §9.4 / G5 — internal retention-sweep endpoint for the
	// vaultdms-retention CronJob.
	retentionSweepMux := http.NewServeMux()
	handler.NewRetentionSweepHandler(svc, *log.Z()).Register(retentionSweepMux)
	rootMux.Handle("POST /internal/v1/retention/sweep",
		middleware.CorrelationHTTP(retentionSweepMux))

	// §10.3 / E6 — OnlyOffice editor config + save callback.
	onlyOfficeMux := http.NewServeMux()
	handler.NewOnlyOfficeHandler(*log.Z()).Register(onlyOfficeMux)
	rootMux.Handle("GET /api/v1/documents/{id}/versions/{vid}/onlyoffice/config",
		middleware.CorrelationHTTP(onlyOfficeMux))
	rootMux.Handle("POST /api/v1/documents/{id}/versions/{vid}/onlyoffice/callback",
		middleware.CorrelationHTTP(onlyOfficeMux))

	// ADR 0065 — WOPI host. Mounted at /wopi/* so editors (Collabora,
	// OnlyOffice over WOPI, future Office Web) hit the spec-compliant
	// surface alongside the legacy /onlyoffice/* path.
	//
	// IMPORTANT: WOPI does NOT carry the gateway signature header —
	// editors call us directly. The middleware chain wrapping rootMux
	// includes RequireGatewaySignature which would 403 every WOPI
	// call, so we mount the WOPI mux at the very top before the
	// signature gate. The access_token query param is the auth
	// boundary instead.
	wopiMux := http.NewServeMux()
	wopiH := handler.NewWOPIHandler(rdb, *log.Z())
	// ADR 0065 — concrete auditor wired to the existing outbox so
	// session_started / session_ended events flow through the same
	// publisher every other audit row uses.
	wopiH.Auditor = handler.NewOutboxWOPIAuditor(pool, database.NewOutboxRepository(), *log.Z())
	wopiH.Register(wopiMux)
	// File resolver wiring is intentionally deferred — the default
	// Resolve/Open/Save impl ties to storage + policy gRPC and is
	// kept out of this PR to limit scope. When the deploy hasn't
	// wired one, GetFile/PutFile return 503 with a clear message.

	// ADR 0065 — POST /api/v1/documents/{id}/versions/{vid}/coauth/start
	// mints WOPI access_tokens for the calling user and returns the
	// iframe URL. This route IS gateway-signed (it's the
	// authenticated kickoff from the FE), so it lives on rootMux.
	coauthStartMux := http.NewServeMux()
	handler.NewCoauthStartHandler(*log.Z()).Register(coauthStartMux)
	rootMux.Handle("POST /api/v1/documents/{id}/versions/{vid}/coauth/start",
		middleware.CorrelationHTTP(coauthStartMux))

	// ADR 0066 — threaded comments + reactions. Real-time fan-out
	// happens via the collaboration WS service which consumes the
	// dms.comment.* outbox subjects we emit on every transition.
	commentsMux := http.NewServeMux()
	handler.NewCommentsHandler(svc, *log.Z()).Register(commentsMux)
	rootMux.Handle("POST /api/v1/documents/{id}/comments",    middleware.CorrelationHTTP(commentsMux))
	rootMux.Handle("GET /api/v1/documents/{id}/comments",     middleware.CorrelationHTTP(commentsMux))
	rootMux.Handle("POST /api/v1/comments/{cid}/replies",     middleware.CorrelationHTTP(commentsMux))
	rootMux.Handle("PATCH /api/v1/comments/{cid}",            middleware.CorrelationHTTP(commentsMux))
	rootMux.Handle("DELETE /api/v1/comments/{cid}",           middleware.CorrelationHTTP(commentsMux))
	rootMux.Handle("POST /api/v1/comments/{cid}/resolve",     middleware.CorrelationHTTP(commentsMux))
	rootMux.Handle("POST /api/v1/comments/{cid}/unresolve",   middleware.CorrelationHTTP(commentsMux))
	rootMux.Handle("POST /api/v1/comments/{cid}/reactions",   middleware.CorrelationHTTP(commentsMux))
	rootMux.Handle("DELETE /api/v1/comments/{cid}/reactions", middleware.CorrelationHTTP(commentsMux))
	rootMux.Handle("GET /api/v1/comments/{cid}/reactions",    middleware.CorrelationHTTP(commentsMux))

	// ADR 0068 — lightweight tasks. Distinct from workflow_tasks
	// (approval-step state) which lives in services/workflow.
	tasksMux := http.NewServeMux()
	handler.NewTasksHandler(svc, *log.Z()).Register(tasksMux)
	rootMux.Handle("POST /api/v1/tasks",                   middleware.CorrelationHTTP(tasksMux))
	rootMux.Handle("GET /api/v1/tasks/mine",               middleware.CorrelationHTTP(tasksMux))
	rootMux.Handle("GET /api/v1/tasks",                    middleware.CorrelationHTTP(tasksMux))
	rootMux.Handle("GET /api/v1/tasks/{id}",               middleware.CorrelationHTTP(tasksMux))
	rootMux.Handle("PATCH /api/v1/tasks/{id}",             middleware.CorrelationHTTP(tasksMux))
	rootMux.Handle("POST /api/v1/tasks/{id}/assign",       middleware.CorrelationHTTP(tasksMux))
	rootMux.Handle("POST /api/v1/tasks/{id}/unassign",     middleware.CorrelationHTTP(tasksMux))
	rootMux.Handle("POST /api/v1/tasks/{id}/complete",     middleware.CorrelationHTTP(tasksMux))
	rootMux.Handle("POST /api/v1/tasks/{id}/reopen",       middleware.CorrelationHTTP(tasksMux))
	rootMux.Handle("POST /api/v1/tasks/{id}/cancel",       middleware.CorrelationHTTP(tasksMux))
	rootMux.Handle("DELETE /api/v1/tasks/{id}",            middleware.CorrelationHTTP(tasksMux))

	// ADR 0068 — hourly sweep. Stamps reminded_at / overdue_notified_at
	// on tasks crossing the 24h-out and overdue thresholds; emits one
	// notify event per claimed row. UPDATE…RETURNING makes the claim
	// + emit pair effectively idempotent (no double-fire on next tick).
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		// Run once on boot so a deploy doesn't wait an hour to send
		// the first reminder after a cold start.
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

	// §17.3 / D10 — annotation CRUD. Pinned method+path patterns so
	// only the annotation surface lands here; other
	// /api/v1/documents/* paths continue to the grpc-gateway.
	annotationsMux := http.NewServeMux()
	handler.NewAnnotationsHandler(svc, *log.Z()).Register(annotationsMux)
	rootMux.Handle("POST /api/v1/documents/{id}/versions/{vid}/annotations",
		middleware.CorrelationHTTP(annotationsMux))
	rootMux.Handle("GET /api/v1/documents/{id}/versions/{vid}/annotations",
		middleware.CorrelationHTTP(annotationsMux))
	rootMux.Handle("PATCH /api/v1/annotations/{id}", middleware.CorrelationHTTP(annotationsMux))
	rootMux.Handle("DELETE /api/v1/annotations/{id}", middleware.CorrelationHTTP(annotationsMux))

	// ADR 0052 — auto-tagging review surface. Pinned method+path so
	// only the auto-tag sub-paths land here; other /api/v1/documents/*
	// continues to the gRPC-gateway.
	autoTagMux := http.NewServeMux()
	handler.NewAutoTagHandler(svc, *log.Z()).Register(autoTagMux)
	rootMux.Handle("GET /api/v1/documents/{id}/tag-suggestions",
		middleware.CorrelationHTTP(autoTagMux))
	rootMux.Handle("POST /api/v1/documents/{id}/tag-suggestions/review",
		middleware.CorrelationHTTP(autoTagMux))
	rootMux.Handle("GET /api/v1/admin/tag-suggestions",
		middleware.CorrelationHTTP(autoTagMux))
	rootMux.Handle("GET /api/v1/admin/auto-tag-config",
		middleware.CorrelationHTTP(autoTagMux))
	rootMux.Handle("PUT /api/v1/admin/auto-tag-config",
		middleware.CorrelationHTTP(autoTagMux))

	// ADR 0053 — smart-routing review surface + admin rule CRUD.
	smartRouteMux := http.NewServeMux()
	handler.NewSmartRouteHandler(svc, *log.Z()).Register(smartRouteMux)
	rootMux.Handle("GET /api/v1/documents/{id}/route-suggestions",
		middleware.CorrelationHTTP(smartRouteMux))
	rootMux.Handle("POST /api/v1/documents/{id}/route-suggestions/{sid}/accept",
		middleware.CorrelationHTTP(smartRouteMux))
	rootMux.Handle("POST /api/v1/documents/{id}/route-suggestions/{sid}/dismiss",
		middleware.CorrelationHTTP(smartRouteMux))
	rootMux.Handle("GET /api/v1/admin/routing-rules",
		middleware.CorrelationHTTP(smartRouteMux))
	rootMux.Handle("POST /api/v1/admin/routing-rules",
		middleware.CorrelationHTTP(smartRouteMux))
	rootMux.Handle("PUT /api/v1/admin/routing-rules/{id}",
		middleware.CorrelationHTTP(smartRouteMux))
	rootMux.Handle("DELETE /api/v1/admin/routing-rules/{id}",
		middleware.CorrelationHTTP(smartRouteMux))
	rootMux.Handle("GET /api/v1/admin/smart-routing-config",
		middleware.CorrelationHTTP(smartRouteMux))
	rootMux.Handle("PUT /api/v1/admin/smart-routing-config",
		middleware.CorrelationHTTP(smartRouteMux))
	rootMux.Handle("GET /api/v1/admin/filing-analytics",
		middleware.CorrelationHTTP(smartRouteMux))

	// ADR 0054 — compliance PII/PHI review surface + admin config.
	// Distinct mux from the legal-hold complianceMux above.
	piiComplianceMux := http.NewServeMux()
	handler.NewComplianceHandler(svc, *log.Z()).Register(piiComplianceMux)
	rootMux.Handle("GET /api/v1/documents/{id}/compliance",
		middleware.CorrelationHTTP(piiComplianceMux))
	rootMux.Handle("POST /api/v1/documents/{id}/compliance/{fid}/review",
		middleware.CorrelationHTTP(piiComplianceMux))
	rootMux.Handle("GET /api/v1/admin/compliance/dashboard",
		middleware.CorrelationHTTP(piiComplianceMux))
	rootMux.Handle("GET /api/v1/admin/compliance/findings",
		middleware.CorrelationHTTP(piiComplianceMux))
	rootMux.Handle("GET /api/v1/admin/compliance/config",
		middleware.CorrelationHTTP(piiComplianceMux))
	rootMux.Handle("PUT /api/v1/admin/compliance/config",
		middleware.CorrelationHTTP(piiComplianceMux))
	rootMux.Handle("POST /api/v1/admin/compliance/rescan/{id}",
		middleware.CorrelationHTTP(piiComplianceMux))

	// ADR 0057 — OCR quality scoring review surface + admin config.
	ocrQualityMux := http.NewServeMux()
	handler.NewOCRQualityHandler(svc, *log.Z()).Register(ocrQualityMux)
	rootMux.Handle("GET /api/v1/documents/{id}/ocr-quality",
		middleware.CorrelationHTTP(ocrQualityMux))
	rootMux.Handle("POST /api/v1/documents/{id}/ocr-quality/{vid}/{page}/review",
		middleware.CorrelationHTTP(ocrQualityMux))
	rootMux.Handle("GET /api/v1/admin/ocr-quality/review-queue",
		middleware.CorrelationHTTP(ocrQualityMux))
	rootMux.Handle("GET /api/v1/admin/ocr-quality/stats",
		middleware.CorrelationHTTP(ocrQualityMux))
	rootMux.Handle("GET /api/v1/admin/ocr-quality/config",
		middleware.CorrelationHTTP(ocrQualityMux))
	rootMux.Handle("PUT /api/v1/admin/ocr-quality/config",
		middleware.CorrelationHTTP(ocrQualityMux))

	// ADR 0058 — anomaly detection review surface (the trigger lives
	// on the intelligence service, POST /api/v1/intelligence/anomaly/run).
	anomalyMux := http.NewServeMux()
	handler.NewAnomalyHandler(svc, *log.Z()).Register(anomalyMux)
	rootMux.Handle("GET /api/v1/admin/anomalies",
		middleware.CorrelationHTTP(anomalyMux))
	rootMux.Handle("GET /api/v1/admin/anomalies/{id}",
		middleware.CorrelationHTTP(anomalyMux))
	rootMux.Handle("POST /api/v1/admin/anomalies/findings/{fid}/resolve",
		middleware.CorrelationHTTP(anomalyMux))
	rootMux.Handle("GET /api/v1/admin/anomaly-config",
		middleware.CorrelationHTTP(anomalyMux))
	rootMux.Handle("PUT /api/v1/admin/anomaly-config",
		middleware.CorrelationHTTP(anomalyMux))

	// ADR 0059 — classification correction surface (single + bulk).
	classifyCorrectionsMux := http.NewServeMux()
	handler.NewClassifyCorrectionHandler(svc, *log.Z()).Register(classifyCorrectionsMux)
	rootMux.Handle("POST /api/v1/documents/{id}/classify/correct",
		middleware.CorrelationHTTP(classifyCorrectionsMux))
	rootMux.Handle("GET /api/v1/documents/{id}/classify/corrections",
		middleware.CorrelationHTTP(classifyCorrectionsMux))
	rootMux.Handle("POST /api/v1/admin/documents/bulk-reclassify",
		middleware.CorrelationHTTP(classifyCorrectionsMux))

	// ADR 0079 — redaction review queue + apply + gated unredacted download.
	redactionReviewMux := http.NewServeMux()
	handler.NewRedactionReviewHandler(svc, *log.Z()).Register(redactionReviewMux)
	rootMux.Handle("GET /api/v1/documents/{id}/redaction-candidates",
		middleware.CorrelationHTTP(redactionReviewMux))
	rootMux.Handle("POST /api/v1/documents/{id}/redaction/candidates/{cid}/review",
		middleware.CorrelationHTTP(redactionReviewMux))
	rootMux.Handle("POST /api/v1/documents/{id}/redaction/apply",
		middleware.CorrelationHTTP(redactionReviewMux))
	rootMux.Handle("GET /api/v1/documents/{id}/versions/{vid}/unredacted",
		middleware.CorrelationHTTP(redactionReviewMux))

	// ADR 0078 — NER read + correction surface.
	nerMux := http.NewServeMux()
	handler.NewNERHandler(svc, *log.Z()).Register(nerMux)
	rootMux.Handle("GET /api/v1/documents/{id}/entities",
		middleware.CorrelationHTTP(nerMux))
	rootMux.Handle("POST /api/v1/documents/{id}/entities/correct",
		middleware.CorrelationHTTP(nerMux))
	rootMux.Handle("GET /api/v1/documents/{id}/entities/corrections",
		middleware.CorrelationHTTP(nerMux))
	rootMux.Handle("GET /api/v1/admin/ner-config",
		middleware.CorrelationHTTP(nerMux))
	rootMux.Handle("PUT /api/v1/admin/ner-config",
		middleware.CorrelationHTTP(nerMux))
	rootMux.Handle("PUT /api/v1/admin/ner-config/api-key",
		middleware.CorrelationHTTP(nerMux))
	rootMux.Handle("DELETE /api/v1/admin/ner-config/api-key",
		middleware.CorrelationHTTP(nerMux))

	// ADR 0060 — active-learning model management surface (owner/admin gated).
	activeLearningMux := http.NewServeMux()
	handler.NewActiveLearningHandler(svc, *log.Z()).Register(activeLearningMux)
	rootMux.Handle("GET /api/v1/admin/models",
		middleware.CorrelationHTTP(activeLearningMux))
	rootMux.Handle("GET /api/v1/admin/models/{id}",
		middleware.CorrelationHTTP(activeLearningMux))
	rootMux.Handle("POST /api/v1/admin/models/{id}/promote",
		middleware.CorrelationHTTP(activeLearningMux))
	rootMux.Handle("POST /api/v1/admin/models/{id}/retire",
		middleware.CorrelationHTTP(activeLearningMux))
	rootMux.Handle("POST /api/v1/admin/models/retrain",
		middleware.CorrelationHTTP(activeLearningMux))
	rootMux.Handle("GET /api/v1/admin/training-examples/stats",
		middleware.CorrelationHTTP(activeLearningMux))
	rootMux.Handle("DELETE /api/v1/admin/training-examples/{id}",
		middleware.CorrelationHTTP(activeLearningMux))
	rootMux.Handle("GET /api/v1/admin/active-learning/config",
		middleware.CorrelationHTTP(activeLearningMux))
	rootMux.Handle("PUT /api/v1/admin/active-learning/config",
		middleware.CorrelationHTTP(activeLearningMux))

	// ERP outbound push (integrations) — these specific gRPC-gateway routes
	// also accept a Bearer API key (per-route scope) so the ERP sync worker
	// can ingest documents service-to-service. Every other gRPC-gateway
	// route stays cookie-only via the catch-all below. Same
	// TenantHTTP + grpcGatewayInject chain so the identity stamped by either
	// auth path propagates into the outbound gRPC metadata.
	apiKeyGateway := func(scope string) http.Handler {
		return middleware.CorrelationHTTP(
			middleware.SessionOrAPIKey(middleware.SessionAuthConfig{Pool: pool}, scope)(
				middleware.TenantHTTP(pool)(grpcGatewayInject(gwMux))))
	}
	// Same chain, plus the Idempotency layer (after auth+tenant so the key is
	// tenant-scoped). Used on the create + version writes so an Idempotency-Key
	// retry replays the original document instead of duplicating it — the
	// server-side counterpart to the ERP dms_sync_log UNIQUE(entity_type,entity_id).
	apiKeyGatewayIdem := func(scope string) http.Handler {
		return middleware.CorrelationHTTP(
			middleware.SessionOrAPIKey(middleware.SessionAuthConfig{Pool: pool}, scope)(
				middleware.TenantHTTP(pool)(
					middleware.Idempotency(pool)(grpcGatewayInject(gwMux)))))
	}
	rootMux.Handle("POST /api/v1/documents", apiKeyGatewayIdem("documents:write"))
	rootMux.Handle("GET /api/v1/documents/{document_id}", apiKeyGateway("documents:read"))
	rootMux.Handle("POST /api/v1/documents/{document_id}/versions", apiKeyGatewayIdem("documents:write"))
	rootMux.Handle("GET /api/v1/workspaces/{workspace_id}/folders", apiKeyGateway("documents:read"))
	rootMux.Handle("POST /api/v1/workspaces/{workspace_id}/folders", apiKeyGateway("documents:write"))

	// All other routes (including gRPC-Gateway) go through default chain.
	// SessionAuthOptional populates ctx from the session cookie when
	// present — that lets TenantHTTP serve browser-native loaders
	// (`<img src>`, `<video src>`, `<a download>`) which can only send
	// cookies and can't attach the X-Tenant-ID header. Standard axios
	// callers continue to work via the header path (checked first).
	rootMux.Handle("/", middleware.RequestLogHTTP(log)(middleware.CorrelationHTTP(
		middleware.SessionAuthOptional(middleware.SessionAuthConfig{Pool: pool})(
			middleware.TenantHTTP(pool)(grpcGatewayInject(gwMux))))))

	// ADR 0065 — WOPI bypass. Editors hit /wopi/* directly (no
	// gateway in front), so we route /wopi/* to wopiMux without the
	// gateway-signature check. Auth is via the access_token query
	// param (see wopi_handler.go IssueWOPIToken).
	wopiAndRoot := http.NewServeMux()
	wopiAndRoot.Handle("/wopi/", wopiMux)
	// FIX-1 (2026-05-31): wrap rootMux with SessionAuthOptional so every
	// authenticated request lands at downstream handlers with a
	// populated auth.UserInfo on ctx (trusted DB-derived role/tenant/
	// user). Handlers using callers()/authedContext() now read from ctx
	// and reject sessionless requests themselves; sessionless paths
	// (anonymous share links, ZT public viewer, OAuth callbacks) still
	// work because Optional just no-ops when the cookie is absent.
	wopiAndRoot.Handle("/",
		middleware.RequireGatewaySignature()(
			middleware.SessionAuthOptional(middleware.SessionAuthConfig{Pool: pool})(rootMux),
		))

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           wopiAndRoot,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	// ---- Outbox publisher --------------------------------------------------
	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)

	// ---- Orphan-document GC -----------------------------------------------
	// Sweeps documents rows whose upload never produced a version (client
	// crash between create-row and complete-upload). Hourly tick, 24h grace.
	orphanGC := janitor.New(pool, *log.Z())
	go orphanGC.Start(ctx)

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	grpcSrv.GracefulStop()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
	outbox.Stop()
	orphanGC.Stop()
}

// ---- deny-all fallback when Policy Service is unreachable -----------------

// denyAllPolicyClient is used if the policy connection fails at startup.
// Every check returns allowed=false so the service fails closed until the
// dependency is available.
type denyAllPolicyClient struct{}

func (denyAllPolicyClient) CheckPermission(ctx context.Context, in *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: false, Reason: "policy service unavailable"}, nil
}

func (denyAllPolicyClient) BatchCheckPermission(ctx context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{Results: make([]*sedocv1.CheckPermissionResponse, 0, len(in.GetChecks()))}
	for range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: false, Reason: "policy service unavailable"})
	}
	return out, nil
}

// dialAuthOpt opens a 5s-deadlined gRPC connection to the auth
// service. Returns nil + warns on failure so the bulk import path
// can degrade gracefully — user / group items return "auth service
// not configured" rather than tripping the whole batch.
func dialAuthOpt(ctx context.Context, log *logger.Logger, addr string) *grpc.ClientConn {
	if addr == "" {
		log.Warn(ctx).Msg("AUTH_SERVICE_ADDR empty; bulk user / group items will be skipped")
		return nil
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(dialCtx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		log.Warn(ctx).Err(err).Str("addr", addr).
			Msg("auth gRPC dial failed; bulk user / group items will be skipped")
		return nil
	}
	return conn
}
