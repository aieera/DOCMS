/// <reference types="vitest" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { TanStackRouterVite } from '@tanstack/router-vite-plugin'
import path from 'path'

// Dev-only: Kong normally injects X-Gateway-Signature. In host mode
// (make run-all) we bypass Kong, so the Vite proxy must add the
// header itself — otherwise every backend 401s via
// pkg/middleware.RequireGatewaySignature. The value MUST match
// VAULTDMS_GATEWAY_SECRET exported by scripts/run-all-services.sh.
const DEV_GATEWAY_SECRET =
  process.env.VAULTDMS_GATEWAY_SECRET || 'dev-only-gateway-secret-rotate-in-prod'

function withSig(target: string) {
  return {
    target,
    changeOrigin: false,
    configure: (proxy: any) => {
      proxy.on('proxyReq', (proxyReq: any) => {
        proxyReq.setHeader('X-Gateway-Signature', DEV_GATEWAY_SECRET)
      })
    },
  }
}

export default defineConfig({
  plugins: [react(), TanStackRouterVite()],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  // ADR 0074 — pre-bundle the GraphQL deps so adding them to a
  // running dev session doesn't trigger Vite's "Outdated Optimize
  // Dep" 504. Without this, the optimizer only re-scans on boot and
  // late-arriving deps land as cache-misses on the next request.
  optimizeDeps: {
    include: ['urql', '@urql/exchange-persisted', 'graphql'],
  },
  server: {
    port: 3000,
    // Two modes:
    //   1. Gateway mode (default in compose-only deploys) — VITE_GATEWAY_URL
    //      points at Kong on :8080 and every request routes through it.
    //   2. Host mode (when backend services run via `make run-all`) — proxy
    //      per-prefix to the host HTTP ports from scripts/run-all-services.sh
    //      because Kong in a container can't reach host Go services.
    // Activated by VITE_PROXY_MODE=host (or VITE_GATEWAY_URL unset AND mode unset).
    proxy:
      process.env.VITE_PROXY_MODE === 'gateway' || process.env.VITE_GATEWAY_URL
        ? { '/api': withSig(process.env.VITE_GATEWAY_URL || 'http://localhost:8080') }
        : {
            '/api/v1/admin/share-links':        withSig('http://localhost:8182'),
            '/api/v1/admin/retention-policies': withSig('http://localhost:8182'),
            '/api/v1/admin/documents':          withSig('http://localhost:8182'),
            // ADR 0094 — DB driver+capability matrix (document svc owns it).
            '/api/v1/admin/platform/db-info':   withSig('http://localhost:8182'),
            // Document-service intelligence-admin endpoints (must list
            // each explicitly because the catch-all `/api/v1/admin`
            // below sends anything else to the auth service).
            '/api/v1/admin/auto-tag-config':    withSig('http://localhost:8182'),
            '/api/v1/admin/tag-suggestions':    withSig('http://localhost:8182'),
            '/api/v1/admin/routing-rules':      withSig('http://localhost:8182'),
            '/api/v1/admin/smart-routing-config': withSig('http://localhost:8182'),
            '/api/v1/admin/filing-analytics':   withSig('http://localhost:8182'),
            '/api/v1/admin/compliance':         withSig('http://localhost:8182'),
            '/api/v1/admin/ocr-quality':        withSig('http://localhost:8182'),
            '/api/v1/admin/anomalies':          withSig('http://localhost:8182'),
            '/api/v1/admin/anomaly-config':     withSig('http://localhost:8182'),
            '/api/v1/admin/models':             withSig('http://localhost:8182'),
            '/api/v1/admin/training-examples':  withSig('http://localhost:8182'),
            '/api/v1/admin/active-learning':    withSig('http://localhost:8182'),
            '/api/v1/admin/ner-config':         withSig('http://localhost:8182'),
            '/api/v1/admin/llm-usage':          withSig('http://localhost:8194'),
            // ADR 0075 — bulk import / export NDJSON facade. Lives
            // in the document service; above the catch-all so the
            // streaming endpoints don't get intercepted by auth.
            '/api/v1/admin/bulk':               withSig('http://localhost:8182'),
            // ADR 0095 — tenant license stub (document service). MUST
            // come before the /api/v1/admin/tenant catch-all below
            // (intelligence) because http-proxy-middleware routes by
            // object-key insertion order, not longest-match.
            '/api/v1/admin/tenant/license':     withSig('http://localhost:8182'),
            // ADR 0081 — tenant LLM config (provider keys, defaults).
            // Intelligence service owns it. MUST come before the
            // /api/v1/admin catch-all below: http-proxy-middleware
            // uses object-key insertion order, not longest-match.
            '/api/v1/admin/tenant':             withSig('http://localhost:8194'),
            // Search-service admin endpoints (ADR 0083 + ADR 0062).
            // Same insertion-order reason — these have to land
            // before the auth catch-all to avoid 404s.
            '/api/v1/admin/permission-propagation-stats': withSig('http://localhost:8184'),
            '/api/v1/admin/ldap':               withSig('http://localhost:8180'),
            // ADR 0087 + 0077 + 0088 — connector service owns
            // email ingestion, per-tenant event streaming, and
            // watched-folder intake. Without these the FE
            // /admin/integrations/{email,events,drop-folder} pages
            // 404 against the auth catch-all.
            '/api/v1/admin/email-configs':      withSig('http://localhost:8190'),
            '/api/v1/admin/event-stream':       withSig('http://localhost:8190'),
            '/api/v1/admin/intake':             withSig('http://localhost:8190'),
            // Intelligence service hosts the on-demand REST surfaces
            // for Doc Q&A, Translation, and Language detection. The
            // catch-all '/api' below routes to auth (:8180), so this
            // explicit rule is required or these requests 404.
            '/api/v1/intelligence':             withSig('http://localhost:8194'),
            '/api/v1/admin/settings':           withSig('http://localhost:8189'),
            '/api/v1/admin':                    withSig('http://localhost:8180'),
            '/api/v1/permissions':              withSig('http://localhost:8181'),
            '/api/v1/documents':                withSig('http://localhost:8182'),
            '/api/v1/workspaces':               withSig('http://localhost:8182'),
            '/api/v1/folders':                  withSig('http://localhost:8182'),
            '/api/v1/shared':                   withSig('http://localhost:8182'),
            '/api/v1/storage':                  withSig('http://localhost:8182'),
            '/api/v1/compliance':               withSig('http://localhost:8182'),
            '/api/v1/privacy':                  withSig('http://localhost:8182'),
            '/api/v1/residency':                withSig('http://localhost:8182'),
            '/api/v1/annotations':              withSig('http://localhost:8182'),
            // ADR 0066 — threaded comments + reactions. Without this
            // rule /api/v1/comments/*/reactions falls through to the
            // /api auth catch-all (8180) which doesn't know about it
            // and 404s. Document service owns the route.
            '/api/v1/comments':                 withSig('http://localhost:8182'),
            '/api/v1/threads':                  withSig('http://localhost:8182'),
            '/api/v1/tags':                     withSig('http://localhost:8182'),
            '/api/v1/tenants/metadata-schema':  withSig('http://localhost:8182'),
            '/api/v1/search':                   withSig('http://localhost:8184'),
            '/api/v1/saved-searches':           withSig('http://localhost:8184'),
            // Search-service federated admin endpoint. (The other
            // search admin entries moved above the /api/v1/admin
            // catch-all to defeat insertion-order misrouting.)
            '/api/v1/platform/search':          withSig('http://localhost:8184'), // ADR 0069 federated
            '/api/v1/audit':                    withSig('http://localhost:8185'),
            '/api/v1/workflows':                withSig('http://localhost:8186'),
            '/api/v1/notifications':            withSig('http://localhost:8187'),
            // ADR 0068 — lightweight tasks live in the document
            // service. Without this entry /api/v1/tasks/mine falls
            // through to the auth catch-all (8180) and 404s.
            '/api/v1/tasks':                    withSig('http://localhost:8182'),
            '/api/v1/signatures':               withSig('http://localhost:8188'),
            // ADR 0074 — GraphQL read gateway. Host-mode port 8191
            // matches scripts/run-all-services.sh's graphql-gateway
            // entry and docker-compose's port mapping. Without this
            // the doc-detail GraphQL fetch falls through to auth
            // (8180) and 404s.
            '/api/v1/graphql':                  withSig('http://localhost:8191'),
            '/api/v1/webhooks':                 withSig('http://localhost:8190'),
            '/api/v1/connectors':               withSig('http://localhost:8190'),
            // ADR 0091 — MCP server lives in its own service now.
            '/api/v1/mcp':                      withSig('http://localhost:8192'),
            // ADR 0090 — iPaaS triggers route per-resource because
            // each lives in the service that owns the underlying
            // table. Object-key insertion order matters here: these
            // MUST sit above the /api catch-all that lands on auth.
            '/api/v1/integrations/triggers/documents':            withSig('http://localhost:8182'),
            '/api/v1/integrations/triggers/signatures/completed': withSig('http://localhost:8188'),
            '/api/v1/integrations/triggers/workflows/completed':  withSig('http://localhost:8186'),
            // ADR 0098 — zero-trust share (admin + public routes both
            // live on the document service). MUST come before /api so
            // it isn't caught by the auth-service catch-all.
            '/api/v1/zt':                       withSig('http://localhost:8182'),
            '/api/v1/admin/share-links/zt':     withSig('http://localhost:8182'),
            // ADR 0099 — contract intelligence graph (document service).
            '/api/v1/contracts':                withSig('http://localhost:8182'),
            // ADR 0101 — cross-format compare (document service).
            '/api/v1/compare':                  withSig('http://localhost:8182'),
            // ADR 0102 — predictive filing (document service).
            '/api/v1/uploads/predict':          withSig('http://localhost:8182'),
            // ADR 0104 — clause library (document service).
            '/api/v1/clauses':                  withSig('http://localhost:8182'),
            '/api':                             withSig('http://localhost:8180'),
            // ADR 0096 — Yjs CRDT WebSocket. Same-origin proxy lets
            // the browser send the dms_session cookie on Upgrade, which
            // the collab service validates against Postgres.
            '/yjs': {
              target: 'ws://localhost:8083',
              ws: true,
              changeOrigin: false,
            },
          },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    globals: true,
    // Test files live next to source under __tests__/ OR as *.test.{ts,tsx}.
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html', 'lcov'],
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/**/*.d.ts',
        'src/routeTree.gen.ts',
        'src/main.tsx',
        'src/test/**',
      ],
    },
  },
})
