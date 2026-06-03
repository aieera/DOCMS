// Package main boots the VaultDMS MCP server (ADR 0091, §12.8).
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

	"github.com/nats-io/nats.go"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/config"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
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

	pool, err := database.NewPool(ctx, cfg.DatabaseURL, database.DefaultPoolConfig())
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("postgres connect")
	}
	defer pool.Close()
	// FIX-7 follow-up: RLS posture gate. Refuses to start when the
	// connection role unexpectedly has BYPASSRLS; set
	// VAULTDMS_ALLOW_BYPASS_RLS=1 in dev to opt in.

	nc, js, err := events.ConnectNATS(cfg.NATSURL)
	if err != nil {
		log.Warn(ctx).Err(err).Msg("nats connect — audit emit will no-op")
	}
	if nc != nil {
		defer nc.Close()
	}

	// MCP server with audit-emit + scope reader wired.
	mcpSrv := mcp.New(mcp.Config{
		Logger:    *log.Z(),
		AuditEmit: makeAuditEmitter(js, serviceName),
		ScopeOf:   auth.GetScopes,
	})

	// Resolve upstream service base URLs. In compose, services are
	// reachable by their container name on port 8080.
	toolCfg := tools.Config{
		SearchBaseURL:       envOr("SEARCH_BASE_URL", "http://search:8080"),
		DocumentBaseURL:     envOr("DOCUMENT_BASE_URL", "http://document:8080"),
		WorkflowBaseURL:     envOr("WORKFLOW_BASE_URL", "http://workflow:8080"),
		IntelligenceBaseURL: envOr("INTELLIGENCE_BASE_URL", "http://intelligence:8080"),
		GatewaySecret:       os.Getenv("VAULTDMS_GATEWAY_SECRET"),
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
	rootMux := http.NewServeMux()
	rootMux.Handle("/api/v1/mcp", apiAuth(mux))
	rootMux.Handle("/api/v1/mcp/sse", apiAuth(mux))

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

// makeAuditEmitter returns a closure that publishes one
// dms.audit.mcp_tool_invoked.v1 NATS message per tool call. Failures
// log but never block the response — audit is fire-and-forget.
func makeAuditEmitter(js nats.JetStreamContext, source string) func(ctx context.Context, toolName string, ok bool, errMsg string) {
	if js == nil {
		return func(context.Context, string, bool, string) {}
	}
	return func(ctx context.Context, toolName string, ok bool, errMsg string) {
		tenantID, _ := auth.GetTenantID(ctx)
		userID, _ := auth.GetUserID(ctx)
		payload := map[string]any{
			"tenant_id":  tenantID.String(),
			"user_id":    userID.String(),
			"tool":       toolName,
			"ok":         ok,
			"error":      errMsg,
			"source":     source,
			"emitted_at": time.Now().UTC().Format(time.RFC3339Nano),
		}
		raw, _ := json.Marshal(payload)
		_, _ = js.Publish("dms.audit.mcp_tool_invoked.v1", raw)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
