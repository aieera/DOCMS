// Scenario 6: Mixed realistic workload — 10K users, 1 hour, all SLIs.
// Simulates a real production day: auth, browse, search, upload, download,
// workflow, notifications, AI.
import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Trend, Counter, Rate } from 'k6/metrics';
import { BASE_URL, headers, randomTenant, randomString } from '../lib/config.js';

// SLI metrics
const authDuration = new Trend('sli_auth_duration', true);
const docReadDuration = new Trend('sli_doc_read_duration', true);
const searchDuration = new Trend('sli_search_duration', true);
const uploadInitDuration = new Trend('sli_upload_init_duration', true);
const downloadDuration = new Trend('sli_download_url_duration', true);
const autocompleteDuration = new Trend('sli_autocomplete_duration', true);
const workflowDuration = new Trend('sli_workflow_duration', true);
const errorRate = new Rate('error_rate');

export const options = {
  scenarios: {
    mixed: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '5m', target: 2000 },
        { duration: '5m', target: 5000 },
        { duration: '5m', target: 10000 },
        { duration: '35m', target: 10000 },
        { duration: '5m', target: 5000 },
        { duration: '5m', target: 0 },
      ],
    },
  },
  thresholds: {
    'sli_doc_read_duration': ['p(99)<150'],
    'sli_search_duration': ['p(99)<300'],
    'sli_upload_init_duration': ['p(99)<200'],
    'sli_download_url_duration': ['p(99)<100'],
    'sli_autocomplete_duration': ['p(99)<50'],
    'error_rate': ['rate<0.01'],
  },
};

export default function () {
  const tenantId = randomTenant();
  const userId = `user-${randomString(8)}`;
  const h = headers(tenantId, userId);

  // Weighted action selection — simulates real user behavior.
  const roll = Math.random();

  if (roll < 0.05) {
    // 5% — Login
    group('auth', () => {
      const res = http.post(`${BASE_URL}/auth/login`, JSON.stringify({
        email: `${userId}@example.com`, password: 'TestPassword123!',
      }), { headers: { 'Content-Type': 'application/json' } });
      authDuration.add(res.timings.duration);
      errorRate.add(res.status >= 500 ? 1 : 0);
    });

  } else if (roll < 0.40) {
    // 35% — Browse documents (most common action)
    group('doc_read', () => {
      const res = http.get(`${BASE_URL}/documents?workspace_id=ws-${tenantId}&page_size=20`, { headers: h });
      docReadDuration.add(res.timings.duration);
      errorRate.add(res.status >= 500 ? 1 : 0);
      check(res, { 'doc list ok': (r) => r.status === 200 });
    });

  } else if (roll < 0.60) {
    // 20% — Search
    group('search', () => {
      const queries = ['report', 'invoice', 'contract', 'policy', 'memo'];
      const q = queries[Math.floor(Math.random() * queries.length)];
      const res = http.post(`${BASE_URL}/search`, JSON.stringify({
        query: q, page_size: 20, highlight: true,
        facets: ['document_class', 'tags'],
      }), { headers: h });
      searchDuration.add(res.timings.duration);
      errorRate.add(res.status >= 500 ? 1 : 0);
    });

  } else if (roll < 0.70) {
    // 10% — Autocomplete
    group('autocomplete', () => {
      const res = http.get(`${BASE_URL}/search/autocomplete?q=${randomString(3)}&limit=10`, { headers: h });
      autocompleteDuration.add(res.timings.duration);
      errorRate.add(res.status >= 500 ? 1 : 0);
    });

  } else if (roll < 0.80) {
    // 10% — Upload initiate
    group('upload', () => {
      const res = http.post(`${BASE_URL}/storage/uploads/initiate`, JSON.stringify({
        filename: `mixed-${randomString(8)}.pdf`,
        mime_type: 'application/pdf',
        size_bytes: 1024 * 100,
      }), { headers: h });
      uploadInitDuration.add(res.timings.duration);
      errorRate.add(res.status >= 500 ? 1 : 0);
    });

  } else if (roll < 0.88) {
    // 8% — Download URL
    group('download', () => {
      const res = http.get(`${BASE_URL}/storage/downloads/url?upload_id=test-${randomString(8)}`, { headers: h });
      downloadDuration.add(res.timings.duration);
      errorRate.add(res.status >= 500 ? 1 : 0);
    });

  } else if (roll < 0.93) {
    // 5% — Workflow tasks
    group('workflow', () => {
      const res = http.get(`${BASE_URL}/workflows/tasks/mine`, { headers: h });
      workflowDuration.add(res.timings.duration);
      errorRate.add(res.status >= 500 ? 1 : 0);
    });

  } else if (roll < 0.96) {
    // 3% — Notifications
    group('notifications', () => {
      http.get(`${BASE_URL}/notifications?limit=20`, { headers: h });
    });

  } else {
    // 4% — AI ask
    group('ai', () => {
      http.post(`${BASE_URL}/intelligence/ask`, JSON.stringify({
        question: 'What are the key terms in the latest contract?',
        scope: 'tenant',
      }), { headers: h });
    });
  }

  sleep(0.5 + Math.random() * 1.5); // 0.5–2s think time
}
