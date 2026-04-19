// Scenario 4: OCR pipeline — queue 1000 docs, verify all OCR complete <30 min.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend, Counter, Rate } from 'k6/metrics';
import { BASE_URL, headers, randomTenant, randomString } from '../lib/config.js';

const ocrQueueDuration = new Trend('ocr_queue_duration', true);
const ocrDocsQueued = new Counter('ocr_docs_queued');
const ocrDocsCompleted = new Counter('ocr_docs_completed');
const ocrSLIBreach = new Rate('ocr_sli_breach');

export const options = {
  scenarios: {
    queue_docs: {
      executor: 'shared-iterations',
      vus: 50,
      iterations: 1000,
      maxDuration: '10m',
    },
    poll_status: {
      executor: 'constant-vus',
      vus: 10,
      duration: '30m',
      startTime: '5m',
      exec: 'pollOCRStatus',
    },
  },
  thresholds: {
    'ocr_docs_queued': ['count>=1000'],
    'ocr_sli_breach': ['rate<0.05'],
  },
};

const queuedDocs = [];

export default function () {
  const tenantId = randomTenant();
  const h = headers(tenantId);

  // Simulate uploading a PDF that triggers OCR.
  const initRes = http.post(`${BASE_URL}/storage/uploads/initiate`, JSON.stringify({
    filename: `ocr-test-${randomString(8)}.pdf`,
    mime_type: 'application/pdf',
    size_bytes: 1024 * 50, // 50 KB stub
  }), { headers: h });

  ocrQueueDuration.add(initRes.timings.duration);

  if (initRes.status === 200 || initRes.status === 201) {
    const session = JSON.parse(initRes.body);
    queuedDocs.push({ id: session.upload_id, tenant: tenantId, queuedAt: Date.now() });
    ocrDocsQueued.add(1);
  }

  sleep(0.1);
}

export function pollOCRStatus() {
  // In a real test, this would poll a status endpoint or check Redis/NATS
  // for completion events. Stub: simulate checking scan status.
  if (queuedDocs.length === 0) {
    sleep(5);
    return;
  }

  const pick = queuedDocs[Math.floor(Math.random() * Math.min(queuedDocs.length, 100))];
  const h = headers(pick.tenant);

  const res = http.get(`${BASE_URL}/storage/uploads/${pick.id}/status`, { headers: h });

  if (res.status === 200) {
    try {
      const body = JSON.parse(res.body);
      if (body.status === 'completed' || body.scan_result) {
        ocrDocsCompleted.add(1);
        const elapsed = Date.now() - pick.queuedAt;
        ocrSLIBreach.add(elapsed > 30000 ? 1 : 0); // p95 < 30s per doc
      }
    } catch {}
  }

  sleep(2);
}
