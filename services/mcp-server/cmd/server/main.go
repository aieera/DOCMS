// Package main boots the SeDoc MCP server (ADR 0091, §12.8).
//
// Surface:
//   POST /api/v1/mcp         — single JSON-RPC call (curl + admin Test button)
//   GET  /api/v1/mcp/sse     — long-poll SSE stream (Claude Desktop, Cursor)
//   POST /api/v1/mcp/sse     — POST companion for SSE clients
//
// Authentication: API key (Bearer vdms_...) with scope mcp:read for
// query tools, mcp:write for mutating ones (upload, start_workflow).
//
// Tools dispatched to the existing service mesh via HTTP. See
// services/mcp-server/internal/tools/tools.go.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/config"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/services/mcp-server/internal/handler"
	"github.com/aieera/sedoc/services/mcp-server/internal/mcp"
	"github.com/aieera/sedoc/services/mcp-server/internal/tools"
)

const serviceName = "mcp-server"

var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(serviceName)
	if err != nil {
		panic("config load: " + err.Error())
	}
	log := logger.New(serviceName, cfg.ServiceVersion, cfg.LogLevel)

	// License (ADR 0095): load + hourly re-validate so RequireLicenseFeature
	// below can gate the MCP surface. Absent license = unlicensed-dev (gates
	// no-op); an invalid JWT is fatal.
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

	nc, _, err := events.ConnectNATS(cfg.NATSURL)
	if err != nil {
		log.Warn(ctx).Err(err).Msg("nats connect — audit emit will no-op")
	}
	if nc != nil {
		defer nc.Close()
	}

	// MCP server with audit-emit + scope reader wired.
	mcpSrv := mcp.New(mcp.Config{
		Logger:    *log.Z(),
		AuditEmit: makeAuditEmitter(pool, serviceName),
		ScopeOf:   auth.GetScopes,
	})

	// Resolve upstream service base URLs. In compose, services are
	// reachable by their container name on port 8080.
	toolCfg := tools.Config{
		SearchBaseURL:       envOr("SEARCH_BASE_URL", "http://search:8080"),
		DocumentBaseURL:     envOr("DOCUMENT_BASE_URL", "http://document:8080"),
		WorkflowBaseURL:     envOr("WORKFLOW_BASE_URL", "http://workflow:8080"),
		IntelligenceBaseURL: envOr("INTELLIGENCE_BASE_URL", "http://intelligence:8080"),
		GatewaySecret:       os.Getenv("SEDOC_GATEWAY_SECRET"),
		HTTP:                &http.Client{Timeout: 30 * time.Second},
	}
	for _, t := range tools.All(toolCfg) {
		mcpSrv.Register(t)
	}

	// HTTP routes. APIKeyAuth gates the MCP endpoints with mcp:read at
	// the route boundary; per-tool scopes (read vs write) are checked
	// inside the dispatcher.
	mux := http.NewServeMux()
	h := handler.New(mcpSrv, *log.Z())
	h.Register(mux)

	apiAuth := middleware.APIKeyAuth(middleware.APIKeyAuthConfig{
		Pool:          pool,
		RequiredScope: "mcp:read",
	})
	// Per-tenant rate limit. MCP keys are programmatic (Claude Desktop /
	// Cursor / curl); 60/min/tenant caps a tenant's call volume without
	// per-IP pooling. Nested INSIDE APIKeyAuth so the key's tenant is on
	// ctx; for SSE this caps connects, not streamed events. Overridable via
	// redis ratelimit:config:{tenant}:mcp.
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisURL, Password: cfg.RedisPassword, DB: cfg.RedisDB})
	defer func() { _ = rdb.Close() }()
	mcpRL := middleware.NewRateLimiter(rdb, 60)
	mcpLimit := middleware.RateLimitPerTenantHTTP(mcpRL, "mcp")
	// License gate (ADR 0095): the MCP surface is a licensed feature. 402 when
	// the `mcp` flag isn't in the license; no-op in unlicensed-dev. Innermost
	// so auth + rate-limit run first (a tenant's key is established before we
	// answer "not licensed").
	mcpFeature := middleware.RequireLicenseFeature("mcp")
	rootMux := http.NewServeMux()
	rootMux.Handle("/api/v1/mcp", apiAuth(mcpLimit(mcpFeature(mux))))
	rootMux.Handle("/api/v1/mcp/sse", apiAuth(mcpLimit(mcpFeature(mux))))

	// BUG-08 — response security headers, wrapped outermost so they also
	// land on the 401/403/429 responses written by the middleware below.
	secHeaders := middleware.SecurityHeaders(middleware.SecurityHeadersFromConfig(cfg))
	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           secHeaders(middleware.RequireGatewaySignature()(rootMux)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	hs := health.NewServerWithMeta("mcp-server", cfg.Region, pool, nil, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	log.Info(ctx).Str("version", version).Int("tools", len(tools.All(toolCfg))).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
}

// makeAuditEmitter returns a closure that records one
// dms.audit.mcp_tool_invoked.v1 event per tool call. Per §4.7 (outbox-only)
// it writes to the transactional `outbox` table rather than publishing to
// NATS directly — the shared outbox publisher forwards it (dms.audit.> →
// AUDIT_EVENTS). Fire-and-forget: a failed insert logs nothing and never
// blocks the response (audit is best-effort, not on the hot path).
func makeAuditEmitter(pool *pgxpool.Pool, source string) func(ctx context.Context, toolName string, ok bool, errMsg string) {
	if pool == nil {
		return func(context.Context, string, bool, string) {}
	}
	outbox := database.NewOutboxRepository()
	return func(ctx context.Context, toolName string, ok bool, errMsg string) {
		tenantID, err := auth.GetTenantID(ctx)
		if err != nil || tenantID == uuid.Nil {
			return // no tenant on ctx → nothing to attribute the audit to
		}
		userID, _ := auth.GetUserID(ctx)
		// Aggregate = the actor (or the tenant when unauthenticated); the
		// outbox Insert stamps actor_id/ip from ctx for the audit consumer.
		aggregate := userID
		if aggregate == uuid.Nil {
			aggregate = tenantID
		}
		raw, _ := json.Marshal(map[string]any{
			"tenant_id":  tenantID.String(),
			"user_id":    userID.String(),
			"tool":       toolName,
			"ok":         ok,
			"error":      errMsg,
			"source":     source,
			"emitted_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
		evt := database.NewOutboxEvent(tenantID, "dms.audit.mcp_tool_invoked.v1", "mcp_tool", aggregate, raw)
		_ = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			return outbox.Insert(ctx, tx, evt)
		})
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
