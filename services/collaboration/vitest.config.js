import { defineConfig } from 'vitest/config'

// Vitest config for the collaboration WS service — mirrors web/'s setup
// (v8 coverage, lcov + html + text reporters) so CI treats it the same
// as the other JS suite.
export default defineConfig({
  test: {
    // WebSocket + Yjs harness runs in a Node environment (no DOM).
    environment: 'node',
    include: ['test/**/*.test.js'],
    // The service requires SEDOC_GATEWAY_SECRET and SEDOC_DATABASE_URL
    // (config.js). The auth/document fetches are stubbed in the harness and
    // pg is module-mocked wherever persistence runs, so neither value is
    // ever used against anything real.
    env: {
      SEDOC_GATEWAY_SECRET: 'test-gateway-secret',
      SEDOC_DATABASE_URL: 'postgres://test:test@127.0.0.1:1/unused',
    },
    // WS round-trips + CRDT convergence poll on real timers; give them room.
    testTimeout: 15000,
    hookTimeout: 15000,
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html', 'lcov'],
      reportsDirectory: 'coverage',
      include: ['src/**/*.js'],
      // redis.js and nats-bridge.js need live Redis/NATS and are exercised
      // only in the running service, not the unit suite; index.js is the
      // process bootstrap. Keep them out of the ratio so it reflects the
      // logic actually under test (handler, connections, yjs-server,
      // yjs-persistence, comments-api).
      exclude: ['src/index.js', 'src/redis.js', 'src/nats-bridge.js'],
    },
  },
})
