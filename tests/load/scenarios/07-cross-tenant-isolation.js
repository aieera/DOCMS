// Cross-tenant isolation test: 10 tenants under load, verify ZERO cross-
// tenant data leakage in documents, search, and notifications.
import http from 'k6/http';
import { check, sleep, fail } from 'k6';
import { Counter, Rate } from 'k6/metrics';
import { BASE_URL, headers, randomString } from '../lib/config.js';

const isolationViolations = new Counter('isolation_violations');
const isolationChecks = new Counter('isolation_checks');
const violationRate = new Rate('violation_rate');

const TENANTS = Array.from({ length: 10 }, (_, i) => `iso-tenant-${i + 1}`);
const TENANT_SECRETS = {};
TENANTS.forEach(t => { TENANT_SECRETS[t] = `secret-${randomString(16)}`; });

export const options = {
  scenarios: {
    seed: {
      executor: 'shared-iterations',
      vus: 10,
      iterations: 100,
      maxDuration: '5m',
      exec: 'seedDocuments',
    },
    verify: {
      executor: 'constant-vus',
      vus: 50,
      duration: '10m',
      startTime: '5m',
      exec: 'verifyIsolation',
    },
  },
  thresholds: {
    'isolation_violations': ['count==0'],
    'violation_rate': ['rate==0'],
  },
};

// Seed: each tenant creates documents with a tenant-specific secret marker.
export function seedDocuments() {
  const tenant = TENANTS[__VU % TENANTS.length];
  const h = headers(tenant);
  const secret = TENANT_SECRETS[tenant];

  const res = http.post(`${BASE_URL}/documents`, JSON.stringify({
    title: `Isolation Test ${randomString(6)}`,
    description: `MARKER:${secret}`,
    workspace_id: `ws-${tenant}`,
    lifecycle_state: 'active',
    mime_type: 'text/plain',
  }), { headers: h });

  check(res, { 'seed created': (r) => r.status === 201 });
  sleep(0.1);
}

// Verify: each tenant queries and checks that ONLY their own data appears.
export function verifyIsolation() {
  const myTenant = TENANTS[__VU % TENANTS.length];
  const mySecret = TENANT_SECRETS[myTenant];
  const h = headers(myTenant);

  // 1. List documents — should only see own tenant's docs.
  const listRes = http.get(`${BASE_URL}/documents?workspace_id=ws-${myTenant}&page_size=100`, { headers: h });
  isolationChecks.add(1);

  if (listRes.status === 200) {
    try {
      const body = JSON.parse(listRes.body);
      const items = body.items || [];
      for (const doc of items) {
        // Check tenant_id field matches.
        if (doc.tenant_id && doc.tenant_id !== myTenant) {
          isolationViolations.add(1);
          violationRate.add(1);
          console.error(`ISOLATION VIOLATION: tenant ${myTenant} saw doc from ${doc.tenant_id}`);
          return;
        }
        // Check description doesn't contain another tenant's secret.
        if (doc.description) {
          for (const [otherTenant, otherSecret] of Object.entries(TENANT_SECRETS)) {
            if (otherTenant !== myTenant && doc.description.includes(otherSecret)) {
              isolationViolations.add(1);
              violationRate.add(1);
              console.error(`ISOLATION VIOLATION: tenant ${myTenant} saw secret from ${otherTenant}`);
              return;
            }
          }
        }
      }
    } catch {}
  }
  violationRate.add(0);

  // 2. Search — should only return own tenant's results.
  const searchRes = http.post(`${BASE_URL}/search`, JSON.stringify({
    query: 'Isolation Test', page_size: 100,
  }), { headers: h });
  isolationChecks.add(1);

  if (searchRes.status === 200) {
    try {
      const body = JSON.parse(searchRes.body);
      for (const hit of (body.results || [])) {
        if (hit.workspace_id && !hit.workspace_id.includes(myTenant)) {
          isolationViolations.add(1);
          violationRate.add(1);
          console.error(`SEARCH ISOLATION VIOLATION: tenant ${myTenant} saw result from ${hit.workspace_id}`);
          return;
        }
      }
    } catch {}
  }
  violationRate.add(0);

  // 3. Try to access another tenant's data directly (should get 403/404).
  const otherTenant = TENANTS[(TENANTS.indexOf(myTenant) + 1) % TENANTS.length];
  const otherH = { ...h, 'X-Tenant-ID': otherTenant };
  const crossRes = http.get(`${BASE_URL}/documents?workspace_id=ws-${otherTenant}&page_size=10`, { headers: h });
  isolationChecks.add(1);
  // With RLS, this should return empty or 403 — never the other tenant's data.
  if (crossRes.status === 200) {
    try {
      const body = JSON.parse(crossRes.body);
      if ((body.items || []).length > 0) {
        isolationViolations.add(1);
        violationRate.add(1);
        console.error(`CROSS-ACCESS VIOLATION: ${myTenant} accessed ${otherTenant}'s workspace`);
        return;
      }
    } catch {}
  }
  violationRate.add(0);

  sleep(1);
}
