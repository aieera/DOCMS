// Scenario 13 — Concurrent signing (ADR 0105, §16 + ADR 0071).
//
// Models the e-sign flow at scale: sender creates an envelope, adds
// a recipient, sends it, the recipient opens the signing URL,
// signs, the completion webhook fires. ESIGN_MOCK_OK=true is
// assumed in the SUT so we don't hit a real DocuSign quota.
//
// SLOs validated:
//   - sign_create_p95 < 400ms
//   - api_p95 < 200ms / p99 < 500ms for everything else
//   - error_rate < 0.1%
//
// Caveats:
//   - The mock callback is async (NATS → workflow); we record
//     CREATE → SEND → SIGN durations and a separate end-to-end
//     "envelope completed" gauge by polling. Polling is the weakest
//     link of this scenario — a real campaign would subscribe to
//     dms.signature.completed.v1 instead, but k6 has no NATS client.
import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';
import exec from 'k6/execution';

import { BASE_URL, TARGET_VUS, HOLD_DURATION } from '../lib/config.js';
import { authedHeaders, ensureSessionOnce, pickTenant } from '../lib/session.js';

const envCreateDur  = new Trend('sli_sign_create',     true);
const envSendDur    = new Trend('sli_sign_send',       true);
const recipSignDur  = new Trend('sli_sign_recipient',  true);
const envPollDur    = new Trend('sli_sign_completion', true);
const envCompleted  = new Counter('sign_envelopes_completed');
const errors        = new Rate('error_rate');

export const options = {
  scenarios: {
    concurrent_signing: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages:   [
        // 5% slice of TARGET_VUS — signing is the rarest of the four.
        { duration: '10m', target: Math.ceil(TARGET_VUS * 0.005) },
        { duration: '20m', target: Math.ceil(TARGET_VUS * 0.05) },
        { duration: HOLD_DURATION, target: Math.ceil(TARGET_VUS * 0.05) },
        { duration: '20m', target: 0 },
      ],
      gracefulRampDown: '30s',
    },
  },
  thresholds: {
    'sli_sign_create':     ['p(95)<400'],
    'sli_sign_send':       ['p(95)<400'],
    'sli_sign_recipient':  ['p(95)<600'],
    'sli_sign_completion': ['p(95)<5000'],   // includes async callback
    'error_rate':          ['rate<0.001'],
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

  // Find an existing doc to attach the envelope to.
  const docList = http.get(`${BASE_URL}/documents?limit=1`, { headers: h, tags: { endpoint: 'doc_list_signing' } });
  if (docList.status !== 200) { errors.add(1); return; }
  const docs = docList.json()?.documents ?? docList.json()?.items ?? [];
  if (docs.length === 0) return;
  const docID = docs[0].id;

  // 1. CreateEnvelope.
  let envelopeID = null;
  group('sign_create', () => {
    const res = http.post(`${BASE_URL}/signatures/envelopes`,
      JSON.stringify({
        document_id: docID,
        title:       `LoadTest signing ${exec.scenario.iterationInTest}`,
        recipients:  [{
          name:  `Recipient ${vu}`,
          email: `load+recipient+${tenantID}+${vu}@vaultdms.test`,
          role:  'signer',
          order: 1,
        }],
      }),
      { headers: h, tags: { endpoint: 'sign_create' } });
    envCreateDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
    if (res.status >= 200 && res.status < 300) envelopeID = res.json()?.id;
  });
  if (!envelopeID) return;

  // 2. Send the envelope.
  group('sign_send', () => {
    const res = http.post(`${BASE_URL}/signatures/envelopes/${envelopeID}/send`,
      '{}',
      { headers: h, tags: { endpoint: 'sign_send' } });
    envSendDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
  });

  // 3. Recipient opens the signing URL and signs (mock path —
  //    the mock provider auto-completes if X-Mock-Sign: yes is set).
  let recipientToken;
  {
    const res = http.get(`${BASE_URL}/signatures/envelopes/${envelopeID}/recipient-token`,
      { headers: h, tags: { endpoint: 'sign_recipient_token' } });
    if (res.status === 200) recipientToken = res.json()?.token;
  }
  if (recipientToken) {
    group('sign_recipient', () => {
      const res = http.post(`${BASE_URL}/signatures/sign?token=${recipientToken}`,
        JSON.stringify({ accept: true }),
        { headers: { 'Content-Type': 'application/json', 'X-Mock-Sign': 'yes' }, tags: { endpoint: 'sign_recipient_sign' } });
      recipSignDur.add(res.timings.duration);
      errors.add(res.status >= 500 ? 1 : 0);
    });
  }

  // 4. Poll for completion. ESIGN_MOCK_OK auto-completes within ~1s
  //    via the workflow worker; we poll 5x at 1s intervals before
  //    declaring the envelope unfinished (logged as a Counter so
  //    completion-rate ≠ 1.0 is visible on the dashboard).
  const tPollStart = Date.now();
  let completed = false;
  for (let i = 0; i < 5; i++) {
    sleep(1.0);
    const res = http.get(`${BASE_URL}/signatures/envelopes/${envelopeID}`,
      { headers: h, tags: { endpoint: 'sign_envelope_status' } });
    if (res.status === 200 && (res.json()?.status === 'completed' || res.json()?.state === 'completed')) {
      completed = true;
      break;
    }
  }
  envPollDur.add(Date.now() - tPollStart);
  if (completed) envCompleted.add(1);

  sleep(0.5 + Math.random() * 1.5);
}
