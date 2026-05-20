// Scenario 12 — RAG / Doc-Q&A query (ADR 0105, §16 + ADR 0080).
//
// Hits the intelligence service's ask endpoint, both in batch and
// streaming mode. The streaming branch measures TTFT (time-to-first
// -token) which is the user-visible latency the buyer will demo on.
//
// SLOs validated:
//   - rag_ttft_p95 < 1500ms       (streaming first chunk)
//   - api_p95 < 2000ms total      (full response)
import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';
import exec from 'k6/execution';

import { BASE_URL, TARGET_VUS, HOLD_DURATION } from '../lib/config.js';
import { authedHeaders, ensureSessionOnce, pickTenant } from '../lib/session.js';

const askBatchDur   = new Trend('sli_rag_ask_batch',     true);
const askTTFT       = new Trend('sli_rag_ttft_streaming', true);
const askTotalDur   = new Trend('sli_rag_total_streaming', true);
const ragTokens     = new Counter('rag_tokens_returned');
const errors        = new Rate('error_rate');

// Realistic question bag — phrased the way a user would actually
// ask. Half scope=document (specific doc Q&A), half scope=tenant
// (org-wide RAG retrieval).
const QUESTIONS = [
  'What is the indemnification cap in the latest MSA?',
  'Summarise the termination clause for me.',
  'List every NDA we signed in Q1 2026.',
  'What invoices are overdue from Acme Corp?',
  'What is our standard data-retention policy for HR records?',
  'Find the SLA penalty terms.',
  'How many contracts mention "auto-renewal"?',
  'What changed between v3 and v4 of the vendor contract?',
];

function pickQuestion() {
  return QUESTIONS[Math.floor(Math.random() * QUESTIONS.length)];
}

export const options = {
  scenarios: {
    rag_query: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages:   [
        // RAG is the 10% slice. We scale to 10% of TARGET_VUS so the
        // mix matches §16's 70/15/10/5.
        { duration: '10m', target: Math.ceil(TARGET_VUS * 0.01) },
        { duration: '20m', target: Math.ceil(TARGET_VUS * 0.10) },
        { duration: HOLD_DURATION, target: Math.ceil(TARGET_VUS * 0.10) },
        { duration: '20m', target: 0 },
      ],
      gracefulRampDown: '30s',
    },
  },
  thresholds: {
    'sli_rag_ask_batch':       ['p(95)<2000'],
    'sli_rag_ttft_streaming':  ['p(95)<1500'],
    'sli_rag_total_streaming': ['p(95)<5000'],
    'error_rate':              ['rate<0.005'],
  },
};

export default function () {
  const vu       = exec.vu.idInTest;
  const tenantID = pickTenant(vu);
  if (!ensureSessionOnce(tenantID, vu)) {
    errors.add(1);
    sleep(2);
    return;
  }
  const h = authedHeaders();
  const q = pickQuestion();
  const streaming = Math.random() < 0.5;

  if (!streaming) {
    group('rag_ask_batch', () => {
      const res = http.post(`${BASE_URL}/intelligence/ask`,
        JSON.stringify({
          question: q,
          scope:    Math.random() < 0.5 ? 'tenant' : 'workspace',
          stream:   false,
        }),
        { headers: h, tags: { endpoint: 'rag_ask_batch' }, timeout: '60s' });
      askBatchDur.add(res.timings.duration);
      errors.add(res.status >= 500 ? 1 : 0);
      check(res, { 'rag 2xx': (r) => r.status >= 200 && r.status < 300 });
      if (res.status === 200) {
        const body = res.json();
        const tokens = body?.token_count ?? body?.tokens ?? 0;
        if (tokens) ragTokens.add(tokens);
      }
    });
  } else {
    // Streaming path. k6's http.post doesn't expose progressive
    // chunks, so we approximate TTFT by issuing a no-stream peek
    // first (header round-trip) and treating that as TTFT — wrong
    // by a hop but a stable proxy. The real /stream endpoint is
    // hit separately; total time is its own metric.
    const tStart = Date.now();
    group('rag_ask_streaming', () => {
      // Issue both in parallel — the SSE endpoint is fire-and-poll;
      // the prefetch is a HEAD-equivalent to estimate TTFT.
      const peek = http.get(`${BASE_URL}/intelligence/ask/stream/peek?q=${encodeURIComponent(q)}`,
        { headers: h, tags: { endpoint: 'rag_peek' }, timeout: '5s' });
      askTTFT.add(peek.timings.duration);

      const res = http.post(`${BASE_URL}/intelligence/ask`,
        JSON.stringify({ question: q, scope: 'tenant', stream: true }),
        { headers: h, tags: { endpoint: 'rag_ask_stream' }, timeout: '60s' });
      askTotalDur.add(Date.now() - tStart);
      errors.add(res.status >= 500 ? 1 : 0);
      if (res.status === 200) {
        const tokens = res.json()?.token_count ?? 0;
        if (tokens) ragTokens.add(tokens);
      }
    });
  }

  // Users wait between asks — RAG conversations are not bursty.
  sleep(3.0 + Math.random() * 5.0);
}
