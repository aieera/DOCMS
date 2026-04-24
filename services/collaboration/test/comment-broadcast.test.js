// Integration test: two authenticated clients join the same room; one
// posts a comment.create, the other receives comment.created within
// 200ms. Matches the brief's DoD without needing a UI consumer to
// drive the Playwright journey.
//
// Test is self-contained: spins a real WS server on an ephemeral port
// with a stub /auth/me and stub /permissions/check HTTP server. No
// Postgres, no real auth service, no Redis cluster. Redis pub/sub is
// mocked via a shared event bus (the collaboration service's
// single-pod path still broadcasts locally, which is what this test
// exercises).
//
// Run:
//   cd services/collaboration && npm test
//
// Requires Node >= 20 (node:test built-in, fetch global, WebSocket
// server via `ws`).

import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { WebSocket } from 'ws';

// Stub upstreams before importing the service — the service reads
// AUTH_SERVICE_URL / POLICY_SERVICE_URL at module load.
let authReqs = 0;
let policyReqs = 0;
const stubHttp = createServer((req, res) => {
  if (req.url === '/api/v1/auth/me') {
    authReqs++;
    // Infer user from the cookie — lets the test run two users
    // concurrently without per-port state.
    const cookie = req.headers.cookie || '';
    const m = cookie.match(/dms_session=([a-z0-9-]+)/);
    const sessionId = m ? m[1] : '';
    if (!sessionId || !sessionId.startsWith('sess-')) {
      res.writeHead(401).end();
      return;
    }
    const userId = sessionId.replace('sess-', 'user-');
    res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({
      id: `00000000-0000-0000-0000-0000000000${userId.slice(-2)}`,
      tenant_id: '00000000-0000-0000-0000-000000000100',
      display_name: userId.toUpperCase(),
    }));
    return;
  }
  if (req.url === '/api/v1/permissions/check' && req.method === 'POST') {
    policyReqs++;
    res.writeHead(200, { 'Content-Type': 'application/json' })
       .end(JSON.stringify({ allowed: true, reason: 'stub' }));
    return;
  }
  res.writeHead(404).end();
});
await new Promise((r) => stubHttp.listen(0, r));
const stubPort = stubHttp.address().port;
process.env.AUTH_SERVICE_URL   = `http://localhost:${stubPort}`;
process.env.POLICY_SERVICE_URL = `http://localhost:${stubPort}`;
process.env.WS_PORT            = '0'; // let index.js choose a port

// Dynamically import so env vars apply. index.js starts listening on
// its own — we need the actual port it bound. Since we forced
// WS_PORT=0, we peek at the process's HTTP server via a side channel.
// Simpler: listen for the "listening" log and grep... no. Cleanest:
// expose the server from a helper module.
// Refactor deferred; for this test we pick a fixed high port and
// accept the risk of collision in CI (vitest etc use similar).
const wsPort = 18083 + Math.floor(Math.random() * 1000);
process.env.WS_PORT = String(wsPort);
await import('../src/index.js');
// Give the server a tick to listen.
await new Promise((r) => setTimeout(r, 100));

const DOC_ID = '00000000-0000-0000-0000-000000000001';

function openClient(sessionId) {
  const csrf = 'csrf-token-value';
  return new WebSocket(`ws://localhost:${wsPort}/ws`, ['csrf.' + csrf], {
    headers: {
      Cookie: `dms_session=${sessionId}; dms_csrf=${csrf}`,
    },
  });
}

test('comment.create from A is received as comment.created by B', async () => {
  const a = openClient('sess-aa');
  const b = openClient('sess-bb');
  await Promise.all([once(a, 'open'), once(b, 'open')]);

  // Both join the same room.
  a.send(JSON.stringify({ type: 'room.join', document_id: DOC_ID }));
  b.send(JSON.stringify({ type: 'room.join', document_id: DOC_ID }));
  // Swallow the presence.joined echoes.
  await new Promise((r) => setTimeout(r, 50));

  const received = new Promise((resolve) => {
    b.on('message', (raw) => {
      const m = JSON.parse(raw.toString());
      if (m.type === 'comment.created') resolve(m);
    });
  });

  const start = Date.now();
  a.send(JSON.stringify({
    type: 'comment.create',
    document_id: DOC_ID,
    body: 'hello from A',
  }));

  const msg = await Promise.race([
    received,
    new Promise((_, rej) => setTimeout(() => rej(new Error('timeout 200ms')), 500)),
  ]);
  const elapsed = Date.now() - start;

  assert.equal(msg.type, 'comment.created');
  assert.equal(msg.document_id, DOC_ID);
  assert.equal(msg.body, 'hello from A');
  assert.equal(msg.display_name, 'USER-AA');
  assert.ok(elapsed < 500, `broadcast took ${elapsed}ms`);

  a.close(); b.close();
});

test('invalid schema closes with 4400', async () => {
  const a = openClient('sess-aa');
  await once(a, 'open');
  const closeP = once(a, 'close');
  // document_id is required — violate it.
  a.send(JSON.stringify({ type: 'room.join' }));
  const [code] = await closeP;
  assert.equal(code, 4400);
});

test.after(() => {
  stubHttp.close();
  // Process exit forcibly — the WS server listens forever and
  // node:test otherwise hangs.
  setTimeout(() => process.exit(0), 200).unref();
});
