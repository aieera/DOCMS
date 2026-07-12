import { defineConfig } from 'vitest/config'

// Vitest config for the collaboration WS service — mirrors web/'s setup
// (v8 coverage, lcov + html + text reporters) so CI treats it the same
// as the other JS suite.
export default defineConfig({
  test: {
    // WebSocket + Yjs harness runs in a Node environment (no DOM).
    environment: 'node',
    include: ['test/**/*.test.js'],
    // yjs-server.js reads SEDOC_GATEWAY_SECRET at import time and
    // process.exit(1)s if it's missing — set it before any test module
    // loads so importing the server under test never kills the worker.
    // The auth/policy fetches are stubbed in the harness, so the value is
    // never sent anywhere real.
    env: {
      SEDOC_GATEWAY_SECRET: 'test-gateway-secret',
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
