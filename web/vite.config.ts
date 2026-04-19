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
    // §3.1 / B2.4 — single proxy entry. All API traffic goes through
    // Kong (deploy/gateway/kong.yaml). Kong owns per-service routing,
    // auth-class gating, rate limits, and the X-Gateway-Signature
    // injection. If the gateway isn't up, requests fail early instead
    // of partially working against a subset of direct-to-backend routes
    // — which is the correct behaviour for a gateway-enforced invariant.
    //
    // Gateway host/port:
    //   * VITE_GATEWAY_URL env override (for remote-gateway dev setups)
    //   * default http://localhost:8080 = the Kong container from compose
    proxy: {
      '/api': process.env.VITE_GATEWAY_URL || 'http://localhost:8080',
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
