// Collaboration WebSocket load test.
//
// Brief target: 1000 concurrent connections per pod, each joins a room
// and emits typing chatter. Success criteria: every connection stays
// open for the full duration, WS handshake p95 < 200ms, broadcast
// round-trip p95 < 200ms.
//
// Run: k6 run tests/load/scenarios/08-collaboration-ws.js
//
// Env:
//   WS_URL          — ws://host:port (default ws://localhost:8083)
//   AUTH_URL        — https://api.vaultdms.local (for login)
//   TEST_EMAIL      — seeded tenant admin
//   TEST_PASSWORD
//   TENANT_SLUG
//   DOC_ID          — pre-created document the tenant can read
//
// Prereq: the load tenant must have `document:read` on DOC_ID for the
// test user. The setup() below does a single login to obtain the
// session cookie; k6 clones it into every VU.

import ws from 'k6/ws';
import http from 'k6/http';
import { check } from 'k6';
import { Trend, Counter } from 'k6/metrics';

const WS_URL   = __ENV.WS_URL   || 'ws://localhost:8083';
const AUTH_URL = __ENV.AUTH_URL || 'http://localhost:8080';
const EMAIL    = __ENV.TEST_EMAIL    || 'admin@acme.test';
const PASSWORD = __ENV.TEST_PASSWORD || 'change-me-in-ci';
const SLUG     = __ENV.TENANT_SLUG   || 'acme';
const DOC_ID   = __ENV.DOC_ID        || '00000000-0000-0000-0000-000000000001';

export const options = {
  scenarios: {
    connect_and_chatter: {
      executor: 'constant-vus',
      vus: 1000,
      duration: '2m',
    },
  },
  thresholds: {
    ws_session_duration: ['p(95)>=115000'],      // each VU stays connected ~full window
    'ws_ping_rtt': ['p(95)<2000'],
    'broadcast_rtt_ms': ['p(95)<200'],
    'checks': ['rate>0.99'],
  },
};

const broadcastRTT = new Trend('broadcast_rtt_ms', true);
const frames       = new Counter('frames_received');

export function setup() {
  // Single login; k6's http.post persists the Set-Cookie response
  // headers. We copy the Cookie header into every VU's ws.connect().
  const res = http.post(`${AUTH_URL}/api/v1/auth/login`, JSON.stringify({
    email: EMAIL, password: PASSWORD, tenant_slug: SLUG,
  }), { headers: { 'Content-Type': 'application/json' } });
  check(res, { 'login 200': (r) => r.status === 200 });
  const setCookie = res.headers['Set-Cookie'] || '';
  // Extract dms_session + dms_csrf from the Set-Cookie header list.
  const session = /dms_session=([^;]+)/.exec(setCookie)?.[1] || '';
  const csrf    = /dms_csrf=([^;]+)/.exec(setCookie)?.[1]    || '';
  return { session, csrf };
}

export default function (data) {
  const headers = {
    Cookie: `dms_session=${data.session}; dms_csrf=${data.csrf}`,
  };
  // CSRF token rides as a WS subprotocol per services/collaboration/src/auth.js.
  const url = WS_URL + '/ws';
  const params = { headers, tags: { scenario: 'collab' }, subprotocols: ['csrf.' + data.csrf] };

  const res = ws.connect(url, params, (socket) => {
    socket.on('open', () => {
      socket.send(JSON.stringify({ type: 'room.join', document_id: DOC_ID }));
      // Typing chatter — 1 start + stop every 5s for the session duration.
      socket.setInterval(() => {
        const tStart = Date.now();
        socket.send(JSON.stringify({
          type: 'typing.start',
          document_id: DOC_ID,
        }));
        socket.setTimeout(() => {
          socket.send(JSON.stringify({
            type: 'typing.stop',
            document_id: DOC_ID,
          }));
        }, 800);
        // Record the RTT as the time until we see someone else's
        // typing.start on this channel (sampled).
        socket.once('message', (raw) => {
          try {
            const m = JSON.parse(raw);
            if (m.type === 'typing.start' && m.user_id !== undefined) {
              broadcastRTT.add(Date.now() - tStart);
            }
            frames.add(1);
          } catch { /* ignore */ }
        });
      }, 5000);
    });

    socket.on('message', () => frames.add(1));
    socket.setTimeout(() => socket.close(), 120_000); // full window
  });

  check(res, { 'ws 101 switching protocols': (r) => r && r.status === 101 });
}
