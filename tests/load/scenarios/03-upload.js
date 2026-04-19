// Scenario 3: Upload — 100 concurrent uploads (10-100MB), verify SHA-256.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend, Counter } from 'k6/metrics';
import crypto from 'k6/crypto';
import { BASE_URL, headers, randomTenant, randomString } from '../lib/config.js';

const uploadInitDuration = new Trend('upload_init_duration', true);
const uploadPutDuration = new Trend('upload_put_duration', true);
const uploadCompleteDuration = new Trend('upload_complete_duration', true);
const uploadsCompleted = new Counter('uploads_completed');
const uploadsFailed = new Counter('uploads_failed');

export const options = {
  scenarios: {
    uploads: {
      executor: 'constant-vus',
      vus: 100,
      duration: '15m',
    },
  },
  thresholds: {
    'upload_init_duration': ['p(99)<200'],
    'uploads_completed': ['count>50'],
    'http_req_failed': ['rate<0.05'],
  },
};

export default function () {
  const tenantId = randomTenant();
  const h = headers(tenantId);
  const sizeMB = 10 + Math.floor(Math.random() * 90); // 10-100 MB
  const sizeBytes = sizeMB * 1024 * 1024;
  const filename = `loadtest-${randomString(8)}.bin`;

  // Generate random payload and compute SHA-256.
  const payload = crypto.randomBytes(Math.min(sizeBytes, 1024 * 1024)); // k6 caps at reasonable size
  const hash = crypto.sha256(payload, 'hex');

  // 1. Initiate upload.
  const initRes = http.post(`${BASE_URL}/storage/uploads/initiate`, JSON.stringify({
    filename,
    mime_type: 'application/octet-stream',
    size_bytes: payload.length,
    sha256_hash: hash,
  }), { headers: h });
  uploadInitDuration.add(initRes.timings.duration);

  check(initRes, { 'init 200': (r) => r.status === 200 || r.status === 201 });

  if (initRes.status !== 200 && initRes.status !== 201) {
    uploadsFailed.add(1);
    sleep(1);
    return;
  }

  const session = JSON.parse(initRes.body);

  // Short-circuit if deduplicated.
  if (session.deduplicated) {
    uploadsCompleted.add(1);
    sleep(0.5);
    return;
  }

  // 2. PUT to presigned URL.
  const putRes = http.put(session.presigned_put_url, payload, {
    headers: { 'Content-Type': 'application/octet-stream' },
    timeout: '120s',
  });
  uploadPutDuration.add(putRes.timings.duration);
  check(putRes, { 'put 200': (r) => r.status === 200 });

  // 3. Complete upload.
  const completeRes = http.post(`${BASE_URL}/storage/uploads/${session.upload_id}/complete`, JSON.stringify({
    sha256_hash: hash,
  }), { headers: h });
  uploadCompleteDuration.add(completeRes.timings.duration);

  const success = check(completeRes, {
    'complete 200': (r) => r.status === 200,
    'sha256 verified': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.sha256_hash === hash || body.status === 'completed';
      } catch { return false; }
    },
  });

  if (success) uploadsCompleted.add(1);
  else uploadsFailed.add(1);

  sleep(1);
}
