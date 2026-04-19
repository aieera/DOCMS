// Package main boots the VaultDMS document service — the Phase-5 reference
// implementation. It wires repositories, service, Policy gRPC client, and
// handler onto a gRPC server, a grpc-gateway REST mux, a dedicated health
// server, and the transactional outbox publisher.
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

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/redis/go-redis/v9"
	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

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
	docHandler := handler.New(svc, *log.Z(), cfg.PublicURL)
	holdsHandler := handler.NewHoldsHandler(holdsService, *log.Z())

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

	storageProxy := handler.NewStorageProxy(storageClient)

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
	gwMux := runtime.NewServeMux()

	// grpcGatewayInject wraps gwMux so X-Tenant-ID / X-User-ID / role
	// HTTP headers land in gRPC metadata. runtime.WithMetadata /
	// WithIncomingHeaderMatcher both rely on grpc-gateway's own
	// propagation which was lossy in practice; this middleware uses
	// metadata.AppendToOutgoingContext directly which grpc-gateway
	// forwards verbatim to the gRPC call.
	grpcGatewayInject := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			md := metadata.MD{}
			get := func(k string) string {
				if v := r.Header.Get(k); v != "" {
					return v
				}
				return ""
			}
			tid := get("X-Tenant-ID")
			if tid == "" {
				tid = get("X-Auth-Tenant-ID")
			}
			if tid != "" {
				md.Set("x-tenant-id", tid)
			}
			if v := get("X-User-ID"); v != "" {
				md.Set("x-user-id", v)
			}
			if v := get("X-User-Role"); v != "" {
				md.Set("x-user-role", v)
			}
			ctx := metadata.NewIncomingContext(r.Context(), md)
			next.ServeHTTP(w, r.WithContext(ctx))
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
	rootMux.Handle("/api/v1/storage/", middleware.CorrelationHTTP(storageMux))

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

	// All other routes (including gRPC-Gateway) go through default chain
	// TenantHTTP sets auth.SetTenantID on the request context from
	// X-Tenant-ID. TenantInterceptor now falls back to that when
	// gRPC metadata is empty — covers host-dev mode where grpc-
	// gateway's header forwarding is lossy.
	rootMux.Handle("/", middleware.RequestLogHTTP(log)(middleware.CorrelationHTTP(middleware.TenantHTTP(pool)(grpcGatewayInject(gwMux)))))

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           middleware.RequireGatewaySignature()(rootMux),
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
