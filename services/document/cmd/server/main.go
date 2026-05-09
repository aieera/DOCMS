// Package main boots the VaultDMS document service — the Phase-5 reference
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

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/redis/go-redis/v9"
	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/vaultdms/vaultdms/pkg/config"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/events"
	"github.com/vaultdms/vaultdms/pkg/health"
	"github.com/vaultdms/vaultdms/pkg/logger"
	"github.com/vaultdms/vaultdms/pkg/middleware"
	"github.com/vaultdms/vaultdms/pkg/storage"

	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
	"github.com/vaultdms/vaultdms/services/document/internal/compliance"
	"github.com/vaultdms/vaultdms/services/document/internal/handler"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
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
	var policyClient vaultdmsv1.PolicyServiceClient
	if policyConn != nil {
		policyClient = vaultdmsv1.NewPolicyServiceClient(policyConn)
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
	var storageClient vaultdmsv1.StorageServiceClient
	if storageConn != nil {
		storageClient = vaultdmsv1.NewStorageServiceClient(storageConn)
		defer func() { _ = storageConn.Close() }()
	}

	// ---- Wire repos → service → handler -----------------------------------
	repos := repository.New(pool)
	holdsService := compliance.NewHoldsService(pool)
	svc := service.New(pool, repos, policyClient, *log.Z())
	svc.SetHoldsChecker(holdsService)
	// ADR 0078 — base64-decode VAULTDMS_LOCAL_KEK so the NER api-key
	// Set/Clear endpoints can encrypt with AES-256-GCM. Same key the
	// auth service uses for MFA secrets and the intelligence worker
	// uses to decrypt the per-tenant LLM key. Empty / wrong-size KEK
	// leaves the path disabled — Set returns 500 with a clear error.
	if kekB64 := os.Getenv("VAULTDMS_LOCAL_KEK"); kekB64 != "" {
		if kek, err := base64.StdEncoding.DecodeString(kekB64); err == nil {
			svc.SetLocalKEK(kek)
		} else {
			log.Warn(ctx).Err(err).Msg("VAULTDMS_LOCAL_KEK base64 decode failed; tenant secrets disabled")
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
	dsrSalt := os.Getenv("VAULTDMS_DSR_ANONYMIZE_SALT")
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
	retentionPolicyHandler := handler.NewRetentionPolicyHandler(pool, *log.Z())

	storageProxy := handler.NewStorageProxy(storageClient, pool)

	// ---- Health ------------------------------------------------------------
	hs := health.NewServer(pool, rdb, nc, s3c)
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
	vaultdmsv1.RegisterDocumentServiceServer(grpcSrv, docHandler)

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
			if tid != "" {
				r.Header.Set("Grpc-Metadata-X-Tenant-Id", tid)
			}
			if v := get("X-User-ID"); v != "" {
				r.Header.Set("Grpc-Metadata-X-User-Id", v)
			}
			if v := get("X-User-Role"); v != "" {
				r.Header.Set("Grpc-Metadata-X-User-Role", v)
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
	if err := vaultdmsv1.RegisterDocumentServiceHandler(ctx, gwMux, gwConn); err != nil {
		log.Fatal(ctx).Err(err).Msg("gateway register")
	}

	// Rate limit only /api/v1/shared/{token} — public, anonymous endpoint.
	// Spec is 10 req/min per IP; burst matches steady rate so a quick
	// click-through (peek + verify-password = 2 calls) doesn't trip the
	// limiter on the first interaction.
	shareLimiter := middleware.NewIPRateLimiter(10, 10, time.Minute)

	// Custom mux to wrap only the share endpoint
	rootMux := http.NewServeMux()
	rootMux.Handle("/api/v1/shared/", shareLimiter(gwMux))

	// Storage REST proxy — forwards to the storage gRPC service. The
	// proxy reads X-Tenant-ID / X-User-ID from the inbound HTTP request
	// (same pattern as the grpc-gateway) and copies them onto outbound
	// gRPC metadata.
	storageMux := http.NewServeMux()
	storageProxy.Register(storageMux)
	// SessionAuth populates auth.UserInfo on ctx from the dms_session
	// cookie; the proxy's outbound() reads tenant + user from ctx and
	// injects them into outbound gRPC metadata. Without this the
	// upstream Kong path wasn't in play (Vite host-mode proxy bypasses
	// Kong) so X-Tenant-ID never reached the proxy and InitiateUpload
	// failed with INVALID_ARGUMENT before doing any work.
	rootMux.Handle("/api/v1/storage/", middleware.CorrelationHTTP(
		middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(storageMux),
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
	shareLinksAdminMux := http.NewServeMux()
	shareLinksAdminHandler.Register(shareLinksAdminMux)
	rootMux.Handle("/api/v1/admin/share-links", middleware.CorrelationHTTP(shareLinksAdminMux))
	rootMux.Handle("/api/v1/admin/share-links/", middleware.CorrelationHTTP(shareLinksAdminMux))
	rootMux.Handle("/api/v1/admin/documents/", middleware.CorrelationHTTP(shareLinksAdminMux))

	// Retention policies admin — Wave 10.
	retentionPolicyMux := http.NewServeMux()
	retentionPolicyHandler.Register(retentionPolicyMux)
	rootMux.Handle("/api/v1/admin/retention-policies", middleware.CorrelationHTTP(retentionPolicyMux))
	rootMux.Handle("/api/v1/admin/retention-policies/", middleware.CorrelationHTTP(retentionPolicyMux))

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

	// All other routes (including gRPC-Gateway) go through default chain
	// TenantHTTP sets auth.SetTenantID on the request context from
	// X-Tenant-ID. TenantInterceptor now falls back to that when
	// gRPC metadata is empty — covers host-dev mode where grpc-
	// gateway's header forwarding is lossy.
	rootMux.Handle("/", middleware.RequestLogHTTP(log)(middleware.CorrelationHTTP(middleware.TenantHTTP(pool)(grpcGatewayInject(gwMux)))))

	// ADR 0065 — WOPI bypass. Editors hit /wopi/* directly (no
	// gateway in front), so we route /wopi/* to wopiMux without the
	// gateway-signature check. Auth is via the access_token query
	// param (see wopi_handler.go IssueWOPIToken).
	wopiAndRoot := http.NewServeMux()
	wopiAndRoot.Handle("/wopi/", wopiMux)
	wopiAndRoot.Handle("/", middleware.RequireGatewaySignature()(rootMux))

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

// ---- deny-all fallback when Policy Service is unreachable -----------------

// denyAllPolicyClient is used if the policy connection fails at startup.
// Every check returns allowed=false so the service fails closed until the
// dependency is available.
type denyAllPolicyClient struct{}

func (denyAllPolicyClient) CheckPermission(ctx context.Context, in *vaultdmsv1.CheckPermissionRequest, _ ...grpc.CallOption) (*vaultdmsv1.CheckPermissionResponse, error) {
	return &vaultdmsv1.CheckPermissionResponse{Allowed: false, Reason: "policy service unavailable"}, nil
}

func (denyAllPolicyClient) BatchCheckPermission(ctx context.Context, in *vaultdmsv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*vaultdmsv1.BatchCheckPermissionResponse, error) {
	out := &vaultdmsv1.BatchCheckPermissionResponse{Results: make([]*vaultdmsv1.CheckPermissionResponse, 0, len(in.GetChecks()))}
	for range in.GetChecks() {
		out.Results = append(out.Results, &vaultdmsv1.CheckPermissionResponse{Allowed: false, Reason: "policy service unavailable"})
	}
	return out, nil
}
