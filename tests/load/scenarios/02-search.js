// Scenario 2: Search — 200 concurrent searches/sec, mixed modes, 15 min.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend, Rate } from 'k6/metrics';
import { BASE_URL, headers, randomTenant, randomString } from '../lib/config.js';

const searchDuration = new Trend('search_duration', true);
const autocompleteDuration = new Trend('autocomplete_duration', true);
const searchSLIBreach = new Rate('search_sli_breach');

const queries = [
  'quarterly revenue report 2025', 'invoice', 'contract agreement',
  'employee handbook', 'financial statement', 'board meeting minutes',
  'purchase order', 'non-disclosure agreement', 'project plan',
  'compliance audit', 'tax return', 'insurance policy',
];

const filterCombos = [
  { lifecycle_state: ['active'] },
  { document_class: ['report', 'invoice'] },
  { tags: ['finance'], created_after: '2025-01-01T00:00:00Z' },
  { mime_type: ['application/pdf'], created_by: 'user-1' },
  {},
];

export const options = {
  scenarios: {
    search: {
      executor: 'constant-arrival-rate',
      rate: 200,
      timeUnit: '1s',
      duration: '15m',
      preAllocatedVUs: 300,
      maxVUs: 500,
    },
    autocomplete: {
      executor: 'constant-arrival-rate',
      rate: 100,
      timeUnit: '1s',
      duration: '15m',
      preAllocatedVUs: 150,
      maxVUs: 300,
      exec: 'autocomplete',
    },
  },
  thresholds: {
    'search_duration': ['p(99)<300'],
    'autocomplete_duration': ['p(99)<50'],
    'http_req_failed': ['rate<0.01'],
    'search_sli_breach': ['rate<0.01'],
  },
};

export default function () {
  const tenantId = randomTenant();
  const h = headers(tenantId);
  const query = queries[Math.floor(Math.random() * queries.length)];
  const filters = filterCombos[Math.floor(Math.random() * filterCombos.length)];
  const facets = ['document_class', 'tags', 'lifecycle_state'];

  const body = JSON.stringify({
    query,
    filters,
    facets,
    page_size: 20,
    highlight: true,
    sort_by: Math.random() > 0.7 ? 'created_at' : 'relevance',
  });

  const res = http.post(`${BASE_URL}/search`, body, { headers: h });
  searchDuration.add(res.timings.duration);
  searchSLIBreach.add(res.timings.duration > 300 ? 1 : 0);

  check(res, {
    'search 200': (r) => r.status === 200,
    'has results': (r) => {
      try { return JSON.parse(r.body).total_count !== undefined; } catch { return false; }
    },
    'p99 < 300ms': (r) => r.timings.duration < 300,
  });

  sleep(0.01);
}

export function autocomplete() {
  const tenantId = randomTenant();
  const h = headers(tenantId);
  const prefix = randomString(3);

  const res = http.get(`${BASE_URL}/search/autocomplete?q=${prefix}&limit=10`, { headers: h });
  autocompleteDuration.add(res.timings.duration);

  check(res, {
    'autocomplete 200': (r) => r.status === 200,
    'p99 < 50ms': (r) => r.timings.duration < 50,
  });
}
