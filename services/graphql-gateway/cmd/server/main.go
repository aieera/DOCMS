// Package main boots the VaultDMS GraphQL read gateway (ADR 0074).
//
// Topology: HTTP-only public surface (POST /graphql), gRPC-only
// upstream surface (document, workflow, collaboration, policy,
// audit). Per-request DataLoader bundle. Persisted-query
// allow-list embedded at build time. Tenant-scoped Redis token-
// bucket rate limit on /graphql.
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

	vaultdmsv1 "github.com/aieera/sedoc/proto/gen/go/vaultdms/v1"

	"github.com/aieera/sedoc/pkg/config"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"

	"github.com/aieera/sedoc/services/graphql-gateway/internal/exec"
	"github.com/aieera/sedoc/services/graphql-gateway/internal/handler"
	"github.com/aieera/sedoc/services/graphql-gateway/internal/loader"
	"github.com/aieera/sedoc/services/graphql-gateway/internal/persisted"
	"github.com/aieera/sedoc/services/graphql-gateway/internal/resolver"
)

const serviceName = "graphql-gateway"

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

	// ---- Persisted-query manifest (boot-blocker if malformed) ---------
	store, err := persisted.Load()
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("persisted-query manifest load failed")
	}
	log.Info(ctx).Int("entries", store.Size()).Msg("persisted-query manifest loaded")

	// ---- Redis (rate-limit only) -------------------------------------
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisURL, Password: cfg.RedisPassword, DB: cfg.RedisDB})
	defer func() { _ = rdb.Close() }()

	// ---- Upstream gRPC clients (each optional at boot) ---------------
	//
	// Lazy dial — `grpc.NewClient` returns immediately and the
	// connection is established (and re-established) on demand by the
	// underlying resolver/balancer. The previous DialContext+WithBlock
	// pattern returned nil whenever the upstream wasn't ready in the
	// 5s window, and `clients.Document` stayed nil for the lifetime of
	// the gateway — every resolver returned null until the process was
	// restarted. Two upshots:
	//   - boot order between gateway and upstream services no longer
	//     matters
	//   - a transient upstream restart self-heals instead of requiring
	//     a gateway restart too
	clients := resolver.Clients{}
	dial := func(name, addr string) *grpc.ClientConn {
		if addr == "" {
			log.Warn(ctx).Str("service", name).Msg("addr empty; field returns null until configured")
			return nil
		}
		conn, err := grpc.NewClient(addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			log.Warn(ctx).Err(err).Str("service", name).Str("addr", addr).
				Msg("upstream client init failed; field returns null until process restart")
			return nil
		}
		return conn
	}
	if c := dial("document", cfg.DocumentServiceAddr); c != nil {
		clients.Document = vaultdmsv1.NewDocumentServiceClient(c)
		defer func() { _ = c.Close() }()
	}
	if c := dial("workflow", cfg.WorkflowServiceAddr); c != nil {
		clients.Workflow = vaultdmsv1.NewWorkflowServiceClient(c)
		defer func() { _ = c.Close() }()
	}
	if c := dial("collaboration", cfg.CollaborationServiceAddr); c != nil {
		clients.Collaboration = vaultdmsv1.NewCollaborationServiceClient(c)
		defer func() { _ = c.Close() }()
	}
	if c := dial("policy", cfg.PolicyServiceAddr); c != nil {
		clients.Policy = vaultdmsv1.NewPolicyServiceClient(c)
		defer func() { _ = c.Close() }()
	}
	if c := dial("audit", cfg.AuditServiceAddr); c != nil {
		clients.Audit = vaultdmsv1.NewAuditServiceClient(c)
		defer func() { _ = c.Close() }()
	}

	// ---- Resolver + executor ----------------------------------------
	rsv := resolver.New(clients, *log.Z())
	executor := &exec.Executor{
		Schema:    exec.MustLoadSchema(),
		Resolvers: rsv,
	}

	// ---- HTTP handler -------------------------------------------------
	allowDev := version == "dev"
	gqlHandler := handler.New(handler.Config{
		Executor:   executor,
		Persisted:  store,
		Resolvers:  rsv,
		AllowDev:   allowDev,
		Logger:     *log.Z(),
		NewLoaders: func() *loader.Loaders { return newLoaders(rsv) },
	})

	// Rate limit per (tenant, "graphql"). Separate bucket from REST
	// so a noisy GraphQL workload can't starve the REST surface or
	// vice versa. Default 60/sec / 1000/min — overridable per-tenant
	// in admin config.
	rl := middleware.NewRateLimiter(rdb, cfg.DefaultRateLimitPerMin)

	mw := middleware.RequireGatewaySignature()
	limited := middleware.RateLimitHTTP(rl, "graphql")(gqlHandler)
	root := mw(limited)

	hs := health.NewServerWithMeta("graphql-gateway", cfg.Region, nil, rdb, nil, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Bool("dev_escape", allowDev).Msg("graphql http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
}

// newLoaders builds a fresh per-request DataLoader bundle. Each
// loader's fetcher closes over the resolver so it can call the
// upstream gRPC. Cache lifetime = handler return.
func newLoaders(_ *resolver.Resolver) *loader.Loaders {
	// v1: bundles are populated lazily — resolvers that opt into a
	// loader allocate the appropriate Loader on first access. The
	// shape stays here so a future migration to per-loader fetchers
	// is a one-line change per loader without touching the executor.
	return &loader.Loaders{}
}
