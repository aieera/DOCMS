// Scenario 14 — Mixed realistic (ADR 0105 §16, the canonical
// validation scenario).
//
// Weights match the blueprint exactly:
//   70% browse-search-open
//   15% upload-workflow-approve
//   10% rag-query
//    5% concurrent-signing
//
// This scenario is what a §16 sign-off run actually executes. The
// four standalone scenarios (10-13) remain useful for isolating a
// regression to one journey; this one is the buyer-facing claim.
//
// VU shape — ramping-arrival-rate so the total req/s tracks
// TARGET_RPS, not VU count. §16 calls for 10k sustained + 50k burst,
// independent of how many VUs are needed to drive that.
import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';
import exec from 'k6/execution';

import {
  BASE_URL, TARGET_VUS, HOLD_DURATION, BURST_MULT, TENANTS,
} from '../lib/config.js';
import { authedHeaders, ensureSessionOnce, pickTenant } from '../lib/session.js';

// Aggregated SLO trends. Per-journey metrics live in their standalone
// scenarios — this scenario rolls them up into the four buckets the
// buyer's checklist asks about.
const tBrowse    = new Trend('mixed_browse_search_open',     true);
const tWrite     = new Trend('mixed_upload_workflow',        true);
const tRag       = new Trend('mixed_rag',                    true);
const tSign      = new Trend('mixed_signing',                true);
const journeys   = new Counter('mixed_journeys_completed');
const errors     = new Rate('error_rate');

const SUSTAINED_RPS = parseInt(__ENV.TARGET_RPS || '10000');
const BURST_RPS     = Math.round(SUSTAINED_RPS * BURST_MULT);

export const options = {
  scenarios: {
    mixed: {
      executor:           'ramping-arrival-rate',
      startRate:          Math.round(SUSTAINED_RPS * 0.05),
      timeUnit:           '1s',
      preAllocatedVUs:    Math.ceil(TARGET_VUS * 0.5),
      maxVUs:             TARGET_VUS,
      stages: [
        { duration: '10m', target: Math.round(SUSTAINED_RPS * 0.05) }, // warm-up @ 5%
        { duration: '20m', target: SUSTAINED_RPS },                    // ramp
        { duration: HOLD_DURATION, target: SUSTAINED_RPS },            // hold §16 sustained
        { duration: '10m', target: BURST_RPS },                        // burst (§16 5×)
        { duration: '20m', target: 0 },                                // cool
      ],
      gracefulStop: '1m',
    },
  },
  thresholds: {
    // §16 envelope across the whole mix.
    'http_req_duration{kind:api}':     ['p(95)<200', 'p(99)<500'],
    'http_req_duration{kind:search}':  ['p(95)<300', 'p(99)<1000'],
    'http_req_duration{kind:upload}':  ['p(95)<2000'],
    'http_req_duration{kind:rag}':     ['p(95)<2000'],
    'http_req_duration{kind:sign}':    ['p(95)<400'],
    'error_rate':                      ['rate<0.001'],
  },
};

// Roll a 0..1 number; pick the journey by the §16 weighting.
function pickJourney() {
  const r = Math.random();
  if (r < 0.70) return 'browse';   // 70%
  if (r < 0.85) return 'write';    // 15%
  if (r < 0.95) return 'rag';      // 10%
  return 'sign';                   //  5%
}

const SEARCH_TERMS = ['invoice', 'msa', 'nda', 'amendment', 'policy', 'report'];
const QUESTIONS    = [
  'What are the key contract terms?',
  'List overdue invoices.',
  'What is our retention policy?',
];

export default function () {
  const vu       = exec.vu.idInTest;
  const tenantID = pickTenant(vu);
  if (!ensureSessionOnce(tenantID, vu)) {
    errors.add(1);
    sleep(2);
    return;
  }
  const h = authedHeaders();

  const journey = pickJourney();
  const tStart  = Date.now();

  try {
    switch (journey) {
      case 'browse':
        browseSearchOpen(h);
        tBrowse.add(Date.now() - tStart);
        break;
      case 'write':
        uploadWorkflowApprove(h);
        tWrite.add(Date.now() - tStart);
        break;
      case 'rag':
        ragQuery(h);
        tRag.add(Date.now() - tStart);
        break;
      case 'sign':
        signEnvelope(h, tenantID, vu);
        tSign.add(Date.now() - tStart);
        break;
    }
    journeys.add(1, { journey });
  } catch (e) {
    errors.add(1);
  }

  // Think time spread — bursty user behavior averaged across mix.
  sleep(0.5 + Math.random() * 2.0);
}

// --- per-journey bodies (slim versions of 10/11/12/13) -------------

function browseSearchOpen(h) {
  group('browse', () => {
    const ws = http.get(`${BASE_URL}/workspaces?limit=20`,
      { headers: h, tags: { kind: 'api', endpoint: 'workspace_list' } });
    errors.add(ws.status >= 500 ? 1 : 0);
    const wsID = (ws.json()?.workspaces ?? ws.json()?.items ?? [])[0]?.id;
    const docs = http.get(
      wsID ? `${BASE_URL}/documents?workspace_id=${wsID}&page_size=20` : `${BASE_URL}/documents?page_size=20`,
      { headers: h, tags: { kind: 'api', endpoint: 'doc_list' } });
    errors.add(docs.status >= 500 ? 1 : 0);

    const term = SEARCH_TERMS[Math.floor(Math.random() * SEARCH_TERMS.length)];
    const s = http.post(`${BASE_URL}/search`,
      JSON.stringify({ query: term, page_size: 20, highlight: true }),
      { headers: h, tags: { kind: 'search', endpoint: 'search' } });
    errors.add(s.status >= 500 ? 1 : 0);
    const first = (s.json()?.results ?? s.json()?.hits ?? [])[0];
    if (first?.document_id) {
      const op = http.get(`${BASE_URL}/documents/${first.document_id}`,
        { headers: h, tags: { kind: 'api', endpoint: 'doc_open' } });
      errors.add(op.status >= 500 ? 1 : 0);
    }
  });
}

// Smaller payload than scenario 11 (100 KB vs 10 MB) — the mixed run
// is RPS-bounded, so we keep the body cheap. The 10 MB upload-budget
// is validated by scenario 11 standalone; here we exercise the same
// chain but lean on volume not size.
const SMALL = new Uint8Array(100 * 1024);
for (let i = 0; i < SMALL.length; i++) SMALL[i] = i & 0xff;

function uploadWorkflowApprove(h) {
  group('write', () => {
    const ws = http.get(`${BASE_URL}/workspaces?limit=1`,
      { headers: h, tags: { kind: 'api', endpoint: 'workspace_list' } });
    const wsID = (ws.json()?.workspaces ?? ws.json()?.items ?? [])[0]?.id;
    if (!wsID) return;

    const fr = http.get(`${BASE_URL}/folders?workspace_id=${wsID}&limit=1`,
      { headers: h, tags: { kind: 'api', endpoint: 'folder_list' } });
    const folderID = (fr.json() ?? [])[0]?.id;

    const doc = http.post(`${BASE_URL}/documents`,
      JSON.stringify({ workspace_id: wsID, folder_id: folderID, title: `mixed-${Date.now()}.pdf`, tags: ['mixed'] }),
      { headers: h, tags: { kind: 'api', endpoint: 'create_doc' } });
    errors.add(doc.status >= 500 ? 1 : 0);
    const docID = doc.json()?.id;
    if (!docID) return;

    const init = http.post(`${BASE_URL}/storage/uploads/initiate`,
      JSON.stringify({ filename: `m-${Date.now()}.pdf`, mime_type: 'application/pdf', size_bytes: SMALL.length, workspace_id: wsID, folder_id: folderID }),
      { headers: h, tags: { kind: 'upload', endpoint: 'upload_init' } });
    errors.add(init.status >= 500 ? 1 : 0);
    const session = init.json();
    if (!session?.presigned_put_url) return;

    const put = http.put(session.presigned_put_url, SMALL.buffer,
      { headers: { 'Content-Type': 'application/pdf' }, tags: { kind: 'upload', endpoint: 'upload_put' } });
    errors.add(put.status >= 500 ? 1 : 0);

    const done = http.post(`${BASE_URL}/storage/uploads/${session.upload_id}/complete`, '{}',
      { headers: h, tags: { kind: 'upload', endpoint: 'upload_complete' } });
    errors.add(done.status >= 500 ? 1 : 0);
    const blobID = done.json()?.content_blob_id ?? session.content_blob_id;
    if (!blobID) return;

    http.post(`${BASE_URL}/documents/${docID}/versions`,
      JSON.stringify({ content_blob_id: blobID, change_summary: 'mixed' }),
      { headers: h, tags: { kind: 'api', endpoint: 'version_create' } });
  });
}

function ragQuery(h) {
  group('rag', () => {
    const q = QUESTIONS[Math.floor(Math.random() * QUESTIONS.length)];
    const res = http.post(`${BASE_URL}/intelligence/ask`,
      JSON.stringify({ question: q, scope: 'tenant', stream: false }),
      { headers: h, tags: { kind: 'rag', endpoint: 'rag_ask' }, timeout: '60s' });
    errors.add(res.status >= 500 ? 1 : 0);
  });
}

function signEnvelope(h, tenantID, vu) {
  group('sign', () => {
    const docList = http.get(`${BASE_URL}/documents?limit=1`,
      { headers: h, tags: { kind: 'api', endpoint: 'doc_list_signing' } });
    const docID = (docList.json()?.documents ?? docList.json()?.items ?? [])[0]?.id;
    if (!docID) return;

    const env = http.post(`${BASE_URL}/signatures/envelopes`,
      JSON.stringify({
        document_id: docID,
        title:       `Mixed signing ${exec.scenario.iterationInTest}`,
        recipients:  [{ name: `R${vu}`, email: `load+sign+${tenantID}+${vu}@vaultdms.test`, role: 'signer', order: 1 }],
      }),
      { headers: h, tags: { kind: 'sign', endpoint: 'sign_create' } });
    errors.add(env.status >= 500 ? 1 : 0);
    const envID = env.json()?.id;
    if (!envID) return;

    const send = http.post(`${BASE_URL}/signatures/envelopes/${envID}/send`, '{}',
      { headers: h, tags: { kind: 'sign', endpoint: 'sign_send' } });
    errors.add(send.status >= 500 ? 1 : 0);
  });
}

export function handleSummary(data) {
  // Emit two artifacts: the full JSON for post-processing, and a
  // short summary the RUNBOOK can paste directly into summary.md.
  const stamp = new Date().toISOString().replace(/[:.]/g, '-');
  return {
    [`results/${stamp}-mixed-full.json`]:  JSON.stringify(data),
    [`results/${stamp}-mixed-short.txt`]:  shortSummary(data),
    'stdout': shortSummary(data),
  };
}

function shortSummary(d) {
  const m = d.metrics;
  const fmt = (k) => (m[k]?.values?.['p(95)'] !== undefined ? `${m[k].values['p(95)'].toFixed(0)}ms` : '-');
  return [
    '',
    '— Mixed-realistic verdict (§16) —',
    `  api p95            ${fmt('http_req_duration{kind:api}')}`,
    `  search p95         ${fmt('http_req_duration{kind:search}')}`,
    `  upload p95         ${fmt('http_req_duration{kind:upload}')}`,
    `  rag p95            ${fmt('http_req_duration{kind:rag}')}`,
    `  sign p95           ${fmt('http_req_duration{kind:sign}')}`,
    `  journeys completed ${m['mixed_journeys_completed']?.values?.count ?? 0}`,
    `  error rate         ${((m['error_rate']?.values?.rate ?? 0) * 100).toFixed(3)}%`,
    `  tenants: ${TENANTS.length}  target_rps: ${SUSTAINED_RPS}  burst_rps: ${BURST_RPS}  hold: ${HOLD_DURATION}`,
    '',
  ].join('\n');
}
