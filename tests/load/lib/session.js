// Session / auth helpers shared across §16 scenarios.
//
// SeDoc uses cookie-based session auth (dms_session) + a Kong
// gateway signature header (X-Gateway-Signature). For a load run we
// bypass Kong and hit the services through a single load-balancer
// vhost, but the gateway signature header is still required by
// pkg/middleware.RequireGatewaySignature on every service.
//
// Login is exchanged for a session cookie that k6's per-VU cookie
// jar will then attach to subsequent requests automatically. We
// short-circuit auth for VUs by using a service-token mode when
// LOAD_TEST_BYPASS_AUTH=1 is set on the SUT — the load-runner pod
// has a shared bearer that maps to a synthetic VU user inside each
// tenant. Without that flag, every VU does a real login at start.
import http from 'k6/http';
import { check } from 'k6';

import { BASE_URL, TENANTS } from './config.js';

// No hardcoded fallback — see services/collaboration/src/yjs-server.js
// for the rationale. Load tests must export SEDOC_GATEWAY_SECRET to
// match the env under test; failing here is loud and obvious.
const GATEWAY_SECRET = __ENV.SEDOC_GATEWAY_SECRET;
if (!GATEWAY_SECRET) {
  throw new Error('SEDOC_GATEWAY_SECRET is required for load tests; export it before running k6');
}

const BYPASS = __ENV.LOAD_TEST_BYPASS_AUTH === '1';
const BYPASS_TOKEN = __ENV.LOAD_TEST_BYPASS_TOKEN || '';

// pickTenant — every VU is pinned to a single tenant for the
// lifetime of the iteration. Spreading VUs across tenants happens
// at scenario level via TARGET_VUS / TENANTS sharding, not here.
export function pickTenant(vuID) {
  return TENANTS[vuID % TENANTS.length];
}

// loadUserCredentials — synthetic load-test user emails follow a
// stable pattern so the seed script and the k6 runner agree on
// who exists. Format: load+<tenantSlug>+<vuShard>@vaultdms.test.
export function loadUserCredentials(tenantID, vuID) {
  // Cap VU IDs to 1000 distinct users per tenant for the corpus —
  // any larger and the seed grows out of band. Different VUs may
  // share a synthetic user; that's fine for read-mostly traffic.
  const shard = vuID % 1000;
  return {
    email:    `load+${tenantID}+${shard}@vaultdms.test`,
    password: 'load-test-only-DO-NOT-USE-IN-PROD-2026',
  };
}

// authedHeaders — gateway signature only. The session cookie comes
// from the per-VU jar after login() succeeds.
export function authedHeaders(extra) {
  const h = {
    'Content-Type':        'application/json',
    'X-Gateway-Signature': GATEWAY_SECRET,
  };
  if (BYPASS && BYPASS_TOKEN) {
    h['Authorization'] = `Bearer ${BYPASS_TOKEN}`;
  }
  if (extra) Object.assign(h, extra);
  return h;
}

// login — exchange email/password for a dms_session cookie on the
// per-VU jar. Returns true on success so the caller can short-circuit
// the iteration when auth is the failure point.
export function login(tenantID, vuID) {
  if (BYPASS) return true;
  const creds = loadUserCredentials(tenantID, vuID);
  const res = http.post(`${BASE_URL}/auth/login`,
    JSON.stringify(creds),
    {
      headers: authedHeaders(),
      tags:    { endpoint: 'auth_login' },
    });
  check(res, { 'login 200': (r) => r.status === 200 });
  return res.status === 200;
}

// ensureSessionOnce — caller's first action per iteration. k6's VU
// model creates a fresh execution context every iteration, so a
// long-running campaign re-logs in periodically. We cache the cookie
// across iterations on the VU's `__VU_STATE__` global so steady-state
// VUs don't pay the auth tax 10× per minute.
export function ensureSessionOnce(tenantID, vuID) {
  // eslint-disable-next-line no-undef
  if (typeof __VU_STATE__ === 'undefined') {
    // eslint-disable-next-line no-undef
    globalThis.__VU_STATE__ = { authed: false, tenantID, vuID };
  }
  // eslint-disable-next-line no-undef
  if (!__VU_STATE__.authed) {
    // eslint-disable-next-line no-undef
    __VU_STATE__.authed = login(tenantID, vuID);
  }
  // eslint-disable-next-line no-undef
  return __VU_STATE__.authed;
}
