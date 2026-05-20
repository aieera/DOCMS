// Scenario 10 — Browse → Search → Open (ADR 0105, §16 read path).
//
// Models the dominant production action: a user lands on the app,
// lists their workspace, searches for something, clicks the top
// result. This is the 70% slice of the mixed-realistic scenario.
//
// SLOs validated:
//   - api_p95 < 200ms / api_p99 < 500ms  (workspace + doc list + get)
//   - search_p95 < 300ms / search_p99 < 1s
//   - autocomplete_p99 < 50ms
import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Trend, Rate } from 'k6/metrics';
import exec from 'k6/execution';

import { BASE_URL, TARGET_VUS, HOLD_DURATION, TENANTS } from '../lib/config.js';
import { authedHeaders, ensureSessionOnce, pickTenant } from '../lib/session.js';

const wsListDur     = new Trend('sli_workspace_list',      true);
const docListDur    = new Trend('sli_doc_list',            true);
const searchDur     = new Trend('sli_search',              true);
const autocompDur   = new Trend('sli_autocomplete',        true);
const docOpenDur    = new Trend('sli_doc_open',            true);
const downloadDur   = new Trend('sli_download_url',        true);
const errors        = new Rate('error_rate');

export const options = {
  scenarios: {
    browse_search_open: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages:   [
        { duration: '10m', target: Math.ceil(TARGET_VUS * 0.05) },  // warm-up
        { duration: '20m', target: TARGET_VUS },                    // ramp
        { duration: HOLD_DURATION, target: TARGET_VUS },            // hold
        { duration: '20m', target: 0 },                             // cool
      ],
      gracefulRampDown: '30s',
    },
  },
  thresholds: {
    'sli_workspace_list': ['p(95)<200', 'p(99)<500'],
    'sli_doc_list':       ['p(95)<200', 'p(99)<500'],
    'sli_search':         ['p(95)<300', 'p(99)<1000'],
    'sli_autocomplete':   ['p(99)<50'],
    'sli_doc_open':       ['p(95)<200', 'p(99)<500'],
    'sli_download_url':   ['p(99)<100'],
    'error_rate':         ['rate<0.001'],
  },
};

// A small bag of words that exist in the seeded corpus — see
// seed.py's TAG_VOCAB. Mixing in random 3-letter prefixes exercises
// the autocomplete branch.
const SEARCH_TERMS = [
  'invoice', 'contract', 'amendment', 'policy', 'report',
  'q1 2026', 'msa acme', 'nda vendor', 'renewal', 'compliance',
];

function pickSearchTerm() {
  return SEARCH_TERMS[Math.floor(Math.random() * SEARCH_TERMS.length)];
}

export default function () {
  const vu       = exec.vu.idInTest;
  const tenantID = pickTenant(vu);
  if (!ensureSessionOnce(tenantID, vu)) {
    errors.add(1);
    sleep(2);
    return;
  }
  const h = authedHeaders();

  // 1. List workspaces (cold-start of a user session).
  let workspaceID = null;
  group('workspace_list', () => {
    const res = http.get(`${BASE_URL}/workspaces?limit=20`, { headers: h, tags: { endpoint: 'workspace_list' } });
    wsListDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
    if (res.status === 200) {
      const body = res.json();
      const list = body?.workspaces ?? body?.items ?? [];
      if (list.length > 0) workspaceID = list[0].id;
    }
  });

  sleep(0.3 + Math.random() * 0.7); // think 0.3-1s

  // 2. List documents in the chosen workspace.
  let firstDocID = null;
  group('doc_list', () => {
    const url = workspaceID
      ? `${BASE_URL}/documents?workspace_id=${workspaceID}&page_size=20`
      : `${BASE_URL}/documents?page_size=20`;
    const res = http.get(url, { headers: h, tags: { endpoint: 'doc_list' } });
    docListDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
    if (res.status === 200) {
      const body = res.json();
      const list = body?.documents ?? body?.items ?? [];
      if (list.length > 0) firstDocID = list[0].id;
    }
  });

  sleep(0.5 + Math.random() * 1.0);

  // 3. Autocomplete + search.
  const prefix = pickSearchTerm().slice(0, 3);
  group('autocomplete', () => {
    const res = http.get(`${BASE_URL}/search/autocomplete?q=${encodeURIComponent(prefix)}&limit=10`,
      { headers: h, tags: { endpoint: 'autocomplete' } });
    autocompDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
  });

  const searchQ = pickSearchTerm();
  let firstHitID = null;
  group('search', () => {
    const body = JSON.stringify({
      query:     searchQ,
      page_size: 20,
      highlight: true,
      facets:    ['document_class', 'tags', 'mime_type'],
    });
    const res = http.post(`${BASE_URL}/search`, body,
      { headers: h, tags: { endpoint: 'search' } });
    searchDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
    check(res, { 'search 2xx': (r) => r.status >= 200 && r.status < 300 });
    if (res.status === 200) {
      const j = res.json();
      const hits = j?.results ?? j?.hits ?? [];
      if (hits.length > 0) firstHitID = hits[0].document_id ?? hits[0].id;
    }
  });

  sleep(0.5 + Math.random() * 1.5);

  // 4. Open the top hit (or fall back to the first doc from step 2).
  const openID = firstHitID || firstDocID;
  if (!openID) return;

  group('doc_open', () => {
    const res = http.get(`${BASE_URL}/documents/${openID}`, { headers: h, tags: { endpoint: 'doc_open' } });
    docOpenDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
  });

  // 5. Half the time the user actually requests the download URL.
  if (Math.random() < 0.5) {
    group('download_url', () => {
      const res = http.get(`${BASE_URL}/documents/${openID}/versions`,
        { headers: h, tags: { endpoint: 'versions_list' } });
      if (res.status === 200) {
        const j = res.json();
        const versions = j?.versions ?? j?.items ?? [];
        if (versions.length > 0) {
          const vID = versions[0].id;
          const dl = http.get(`${BASE_URL}/storage/downloads/${openID}/${vID}`,
            { headers: h, tags: { endpoint: 'download_url' } });
          downloadDur.add(dl.timings.duration);
          errors.add(dl.status >= 500 ? 1 : 0);
        }
      }
    });
  }

  sleep(1.0 + Math.random() * 3.0); // 1-4s reading time
}

export function handleSummary(data) {
  return {
    'stdout': textSummary(data),
    [`results/${new Date().toISOString().replace(/[:.]/g, '-')}-browse.json`]: JSON.stringify(data),
  };
}

// Minimal stdout summary so the CI log shows the verdict without a
// json post-processor. k6's default is also fine; this one is just
// shorter and includes the SLO verdict per metric.
function textSummary(d) {
  const lines = ['', '— Browse-search-open verdict —'];
  const m = d.metrics;
  const fmt = (n) => (n === undefined ? '-' : `${n.toFixed(0)}ms`);
  for (const [name, friendly] of [
    ['sli_workspace_list', 'workspace list'],
    ['sli_doc_list',       'doc list'],
    ['sli_search',         'search'],
    ['sli_autocomplete',   'autocomplete'],
    ['sli_doc_open',       'doc open'],
    ['sli_download_url',   'download URL'],
  ]) {
    const v = m[name]?.values;
    if (!v) continue;
    lines.push(`  ${friendly.padEnd(16)} p50=${fmt(v['p(50)'])}  p95=${fmt(v['p(95)'])}  p99=${fmt(v['p(99)'])}`);
  }
  const err = m['error_rate']?.values?.rate ?? 0;
  lines.push(`  error rate: ${(err * 100).toFixed(3)}%`);
  lines.push(`  tenants: ${TENANTS.length}  VUs: ${TARGET_VUS}  hold: ${HOLD_DURATION}`);
  return lines.join('\n') + '\n';
}
