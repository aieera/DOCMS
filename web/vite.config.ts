/// <reference types="vitest" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { TanStackRouterVite } from '@tanstack/router-vite-plugin'
import path from 'path'

export default defineConfig({
  plugins: [react(), TanStackRouterVite()],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  server: {
    port: 3000,
    proxy: {
      // Prefix routing to per-service HTTP ports. Longer prefixes must
      // come first — http-proxy-middleware resolves on first match.
      // Ports match scripts/run-all-services.sh (8180-series HTTP).

      // --- document service owns the bulk of /admin/* (not auth!) ---
      '/api/v1/admin/share-links':         'http://localhost:8182', // document
      '/api/v1/admin/retention-policies':  'http://localhost:8182', // document
      '/api/v1/admin/documents':           'http://localhost:8182', // document
      // --- billing owns admin/settings ---
      '/api/v1/admin/settings':            'http://localhost:8189', // billing
      // --- auth owns the rest of /admin/* ---
      '/api/v1/admin':                     'http://localhost:8180', // auth (users, groups, sso-configs, tenants)

      // --- policy ---
      '/api/v1/permissions':               'http://localhost:8181',

      // --- document service (and routes it proxies) ---
      '/api/v1/documents':                 'http://localhost:8182',
      '/api/v1/workspaces':                'http://localhost:8182',
      '/api/v1/folders':                   'http://localhost:8182',
      '/api/v1/shared':                    'http://localhost:8182',
      '/api/v1/storage':                   'http://localhost:8182',
      '/api/v1/compliance':                'http://localhost:8182',
      '/api/v1/privacy':                   'http://localhost:8182',
      '/api/v1/residency':                 'http://localhost:8182',
      '/api/v1/tenants/metadata-schema':   'http://localhost:8182',

      // --- search ---
      '/api/v1/search':                    'http://localhost:8184',
      '/api/v1/saved-searches':            'http://localhost:8184',

      // --- audit ---
      '/api/v1/audit':                     'http://localhost:8185',

      // --- workflow ---
      '/api/v1/workflows':                 'http://localhost:8186',

      // --- notification ---
      '/api/v1/notifications':             'http://localhost:8187',

      // --- signature ---
      '/api/v1/signatures':                'http://localhost:8188',

      // --- connector (webhooks, connectors, MCP SSE) ---
      '/api/v1/webhooks':                  'http://localhost:8190',
      '/api/v1/connectors':                'http://localhost:8190',
      '/api/v1/mcp':                       'http://localhost:8190',

      // --- intelligence service is Python + not started; requests 404
      //     until it's running. No proxy entry needed. ---

      // --- catch-all to auth — covers /auth/*, SCIM, anything else ---
      '/api':                              'http://localhost:8180',
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
