/// <reference types="vitest" />
import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import { TanStackRouterVite } from '@tanstack/router-vite-plugin'
import path from 'path'

// Dev-only: Kong normally injects X-Gateway-Signature. In host mode
// (make run-all) we bypass Kong, so the Vite proxy must add the
// header itself — otherwise every backend 401s via
// pkg/middleware.RequireGatewaySignature. The value MUST match
// SEDOC_GATEWAY_SECRET exported by scripts/run-all-services.sh.
//
// SECURITY (BUG-C1): no hardcoded fallback. The previous default
// `dev-only-gateway-secret-rotate-in-prod` was the literal string
// every Go service still accepts as a valid signature, so leaving
// it in the source tree meant anyone with read access could forge
// gateway-authenticated requests against any deployment that hadn't
// rotated. Now we throw on `vite dev` startup if the env var is
// missing. Production builds (`vite build`) skip the check since the
// proxy doesn't run there.
function requireGatewaySecret(): string {
  const v = process.env.SEDOC_GATEWAY_SECRET
  if (!v || v.trim() === '') {
    throw new Error(
      '\nSEDOC_GATEWAY_SECRET is required for the Vite dev proxy.\n' +
        '  → Copy web/.env.example to web/.env.local and fill it in.\n' +
        '  → Generate a fresh value with: openssl rand -hex 32\n' +
        '  → Use the SAME value in scripts/run-all-services.sh.\n',
    )
  }
  return v
}

function withSig(target: string, secret: string) {
  return {
    target,
    changeOrigin: false,
    configure: (proxy: any) => {
      proxy.on('proxyReq', (proxyReq: any) => {
        proxyReq.setHeader('X-Gateway-Signature', secret)
      })
    },
  }
}

export default defineConfig(({ command, mode }) => {
  // Load web/.env.local (and other .env files) into process.env so the
  // gateway secret + proxy vars can live in .env.local as documented,
  // not just in the shell. Empty prefix = load every key, not only VITE_*.
  Object.assign(process.env, loadEnv(mode, process.cwd(), ''))

  // Eager startup check: fail `vite dev` immediately if the env var
  // is missing rather than letting the dev server boot and then 500
  // on the first proxy request. `vite build` skips this — production
  // bundles don't need the dev proxy secret. Vitest also loads the
  // config in serve mode but never runs the proxy, so we skip there.
  const isTest = process.env.VITEST === 'true' || process.env.NODE_ENV === 'test'
  const gatewaySecret = command === 'serve' && !isTest ? requireGatewaySecret() : ''
  const wsig = (target: string) => withSig(target, gatewaySecret)
  // Same-origin MinIO upload proxy. The browser PUTs file bytes straight
  // to the presigned URL returned by InitiateUpload, whose host is the
  // dev MinIO public base (localhost:9000). PUTting there directly is a
  // cross-origin request to a second port, which silently fails whenever
  // the browser cannot reach :9000 (remote / port-forwarded dev hosts),
  // leaving a 0-byte document with no version. Routing the PUT through
  // Vite keeps it same-origin; changeOrigin rewrites the upstream Host to
  // the MinIO target so the S3 `SignedHeaders=host` signature still
  // validates. The frontend's uploadToPresigned() rewrites :9000 URLs to
  // `/s3` (dev only). Override the target with VITE_S3_TARGET if MinIO is
  // not on localhost:9000.
  const s3Proxy = {
    '/s3': {
      target: process.env.VITE_S3_TARGET || 'http://localhost:9000',
      changeOrigin: true,
      rewrite: (p: string) => p.replace(/^\/s3/, ''),
    },
  }
  return ({
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
     host: true,   // or '0.0.0.0'
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
        ? { ...s3Proxy, '/api': wsig(process.env.VITE_GATEWAY_URL || 'http://localhost:8080') }
        : {
            ...s3Proxy,
            '/api/v1/admin/share-links':        wsig('http://localhost:8182'),
            '/api/v1/admin/retention-policies': wsig('http://localhost:8182'),
            '/api/v1/admin/documents':          wsig('http://localhost:8182'),
            // Admin Trash — list soft-deleted, restore, permanent purge.
            '/api/v1/admin/trash':              wsig('http://localhost:8182'),
            // Empty-folder scan + cleanup (Trash page maintenance tool).
            // Document service owns it; without this entry the request
            // falls into the /api/v1/admin auth catch-all and 404s.
            '/api/v1/admin/folders':            wsig('http://localhost:8182'),
            // Document-service admin surfaces that would otherwise fall
            // into the /api/v1/admin auth catch-all and 404 (same class
            // of bug as the templates/analytics gaps fixed 2026-07-04;
            // the guard test in src/test/devProxyCoverage.test.ts now
            // pins every routes.yaml prefix to the right host port).
            '/api/v1/admin/classification':     wsig('http://localhost:8182'),
            '/api/v1/admin/watermark':          wsig('http://localhost:8182'),
            '/api/v1/admin/irm':                wsig('http://localhost:8182'),
            '/api/v1/admin/ediscovery':         wsig('http://localhost:8182'),
            // SIEM forwarding config lives in the audit service.
            '/api/v1/admin/siem':               wsig('http://localhost:8185'),
            // Auth tenant admin. MUST come before the /api/v1/admin/tenant
            // (intelligence) entry below: proxy keys match by insertion-
            // order startsWith, so "tenant" would swallow "tenants".
            '/api/v1/admin/tenants':            wsig('http://localhost:8180'),
            // Admin Compliance overview — live docs/storage/encryption.
            '/api/v1/admin/compliance':         wsig('http://localhost:8182'),
            // ADR 0094 — DB driver+capability matrix (document svc owns it).
            '/api/v1/admin/platform/db-info':   wsig('http://localhost:8182'),
            // Document-service intelligence-admin endpoints (must list
            // each explicitly because the catch-all `/api/v1/admin`
            // below sends anything else to the auth service).
            '/api/v1/admin/auto-tag-config':    wsig('http://localhost:8182'),
            '/api/v1/admin/tag-suggestions':    wsig('http://localhost:8182'),
            '/api/v1/admin/routing-rules':      wsig('http://localhost:8182'),
            '/api/v1/admin/smart-routing-config': wsig('http://localhost:8182'),
            '/api/v1/admin/filing-analytics':   wsig('http://localhost:8182'),
            '/api/v1/admin/ocr-quality':        wsig('http://localhost:8182'),
            '/api/v1/admin/anomalies':          wsig('http://localhost:8182'),
            '/api/v1/admin/anomaly-config':     wsig('http://localhost:8182'),
            '/api/v1/admin/models':             wsig('http://localhost:8182'),
            '/api/v1/admin/training-examples':  wsig('http://localhost:8182'),
            '/api/v1/admin/active-learning':    wsig('http://localhost:8182'),
            '/api/v1/admin/ner-config':         wsig('http://localhost:8182'),
            '/api/v1/admin/llm-usage':          wsig('http://localhost:8194'),
            // ADR 0075 — bulk import / export NDJSON facade. Lives
            // in the document service; above the catch-all so the
            // streaming endpoints don't get intercepted by auth.
            '/api/v1/admin/bulk':               wsig('http://localhost:8182'),
            // ADR 0095 — tenant license stub (document service). MUST
            // come before the /api/v1/admin/tenant catch-all below
            // (intelligence) because http-proxy-middleware routes by
            // object-key insertion order, not longest-match.
            '/api/v1/admin/tenant/license':     wsig('http://localhost:8182'),
            // Migration 000060 — per-tenant upload format allowlist
            // (owned by document service). Same insertion-order rule:
            // must land before /api/v1/admin/tenant catch-all.
            '/api/v1/admin/tenant/upload-policy': wsig('http://localhost:8182'),
            // ADR 0081 — tenant LLM config (provider keys, defaults).
            // Intelligence service owns it. MUST come before the
            // /api/v1/admin catch-all below: http-proxy-middleware
            // uses object-key insertion order, not longest-match.
            '/api/v1/admin/tenant':             wsig('http://localhost:8194'),
            // Search-service admin endpoints (ADR 0083 + ADR 0062).
            // Same insertion-order reason — these have to land
            // before the auth catch-all to avoid 404s.
            '/api/v1/admin/permission-propagation-stats': wsig('http://localhost:8184'),
            '/api/v1/admin/ldap':               wsig('http://localhost:8180'),
            // ADR 0087 + 0077 + 0088 — connector service owns
            // email ingestion, per-tenant event streaming, and
            // watched-folder intake. Without these the FE
            // /admin/integrations/{email,events,drop-folder} pages
            // 404 against the auth catch-all.
            '/api/v1/admin/email-configs':      wsig('http://localhost:8190'),
            '/api/v1/admin/event-stream':       wsig('http://localhost:8190'),
            '/api/v1/admin/intake':             wsig('http://localhost:8190'),
            // Intelligence service hosts the on-demand REST surfaces
            // for Doc Q&A, Translation, and Language detection. The
            // catch-all '/api' below routes to auth (:8180), so this
            // explicit rule is required or these requests 404.
            '/api/v1/intelligence':             wsig('http://localhost:8194'),
            // ERP integration explorer — the native Integrations→ERP screens
            // call /api/v1/integrations/erp/*, proxied by the auth service to
            // the Files BFF (/files/*). Must sit above the /api catch-all.
            '/api/v1/integrations/erp':         wsig('http://localhost:8180'),
            '/api/v1/admin/settings':           wsig('http://localhost:8189'),
            '/api/v1/admin':                    wsig('http://localhost:8180'),
            '/api/v1/permissions':              wsig('http://localhost:8181'),
            '/api/v1/documents':                wsig('http://localhost:8182'),
            '/api/v1/workspaces':               wsig('http://localhost:8182'),
            '/api/v1/folders':                  wsig('http://localhost:8182'),
            '/api/v1/shared':                   wsig('http://localhost:8182'),
            '/api/v1/storage':                  wsig('http://localhost:8182'),
            '/api/v1/compliance':               wsig('http://localhost:8182'),
            '/api/v1/privacy':                  wsig('http://localhost:8182'),
            '/api/v1/residency':                wsig('http://localhost:8182'),
            '/api/v1/annotations':              wsig('http://localhost:8182'),
            // ADR 0066 — threaded comments + reactions. Without this
            // rule /api/v1/comments/*/reactions falls through to the
            // /api auth catch-all (8180) which doesn't know about it
            // and 404s. Document service owns the route.
            '/api/v1/comments':                 wsig('http://localhost:8182'),
            '/api/v1/threads':                  wsig('http://localhost:8182'),
            '/api/v1/tags':                     wsig('http://localhost:8182'),
            '/api/v1/tenants/metadata-schema':  wsig('http://localhost:8182'),
            '/api/v1/search':                   wsig('http://localhost:8184'),
            '/api/v1/saved-searches':           wsig('http://localhost:8184'),
            // Search-service federated admin endpoint. (The other
            // search admin entries moved above the /api/v1/admin
            // catch-all to defeat insertion-order misrouting.)
            '/api/v1/platform/search':          wsig('http://localhost:8184'), // ADR 0069 federated
            '/api/v1/audit':                    wsig('http://localhost:8185'),
            '/api/v1/workflows':                wsig('http://localhost:8186'),
            '/api/v1/notifications':            wsig('http://localhost:8187'),
            // ADR 0068 — lightweight tasks live in the document
            // service. Without this entry /api/v1/tasks/mine falls
            // through to the auth catch-all (8180) and 404s.
            '/api/v1/tasks':                    wsig('http://localhost:8182'),
            // ADR 0118 — workspace templates, saved reports/analytics,
            // records management, and notes/wiki (all document service).
            // Kong got these routes on 2026-07-02 but the host-mode
            // proxy was never updated, so they 404'd against the auth
            // catch-all in dev.
            '/api/v1/templates':                wsig('http://localhost:8182'),
            '/api/v1/analytics':                wsig('http://localhost:8182'),
            '/api/v1/records':                  wsig('http://localhost:8182'),
            '/api/v1/notes':                    wsig('http://localhost:8182'),
            // More document-service prefixes that fell to the /api auth
            // catch-all: selective sync, IRM protected exports, ingestion
            // pipeline, add-in email filing, and the review queue.
            '/api/v1/sync':                     wsig('http://localhost:8182'),
            '/api/v1/irm':                      wsig('http://localhost:8182'),
            '/api/v1/ingest':                   wsig('http://localhost:8182'),
            '/api/v1/integrations/m365':        wsig('http://localhost:8182'),
            '/api/v1/review-queue':             wsig('http://localhost:8182'),
            '/api/v1/signatures':               wsig('http://localhost:8188'),
            // ADR 0074 — GraphQL read gateway. Host-mode port 8191
            // matches scripts/run-all-services.sh's graphql-gateway
            // entry and docker-compose's port mapping. Without this
            // the doc-detail GraphQL fetch falls through to auth
            // (8180) and 404s.
            '/api/v1/graphql':                  wsig('http://localhost:8191'),
            '/api/v1/webhooks':                 wsig('http://localhost:8190'),
            '/api/v1/connectors':               wsig('http://localhost:8190'),
            // ADR 0091 — MCP server lives in its own service now.
            '/api/v1/mcp':                      wsig('http://localhost:8192'),
            // ADR 0090 — iPaaS triggers route per-resource because
            // each lives in the service that owns the underlying
            // table. Object-key insertion order matters here: these
            // MUST sit above the /api catch-all that lands on auth.
            '/api/v1/integrations/triggers/documents':            wsig('http://localhost:8182'),
            '/api/v1/integrations/triggers/signatures/completed': wsig('http://localhost:8188'),
            '/api/v1/integrations/triggers/workflows/completed':  wsig('http://localhost:8186'),
            // ADR 0098 — zero-trust share (admin + public routes both
            // live on the document service). MUST come before /api so
            // it isn't caught by the auth-service catch-all.
            '/api/v1/zt':                       wsig('http://localhost:8182'),
            '/api/v1/admin/share-links/zt':     wsig('http://localhost:8182'),
            // ADR 0099 — contract intelligence graph (document service).
            '/api/v1/contracts':                wsig('http://localhost:8182'),
            // ADR 0101 — cross-format compare (document service).
            '/api/v1/compare':                  wsig('http://localhost:8182'),
            // ADR 0102 — predictive filing (document service).
            '/api/v1/uploads/predict':          wsig('http://localhost:8182'),
            // ADR 0104 — clause library (document service).
            '/api/v1/clauses':                  wsig('http://localhost:8182'),
            // Python preview API (docker-compose preview-api, host 8193 —
            // 819x service-HTTP convention; 8087 is audit's health port).
            // Browser code doesn't call it today (previews stream via the
            // document/storage URL), but routes.yaml declares it public and
            // the devProxyCoverage guard keeps this map in lockstep.
            '/api/v1/previews':                 wsig('http://localhost:8193'),
            '/api':                             wsig('http://localhost:8180'),
            // ADR 0110 — Residency admin page fetches /healthz directly
            // (not under /api) to read the cluster `region` field. Route
            // it at the document service's health port; without this
            // mapping Vite falls through to the SPA shell and the page
            // shows "Cluster region unknown" forever.
            '/healthz':                         wsig('http://localhost:8084'),
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
})
