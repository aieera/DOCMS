// Shared config and helpers for all k6 load test scenarios.
export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080/api/v1';
export const WS_URL = __ENV.WS_URL || 'ws://localhost:8083/ws';

export const TENANTS = (__ENV.TENANTS || '10').split(',').length > 1
  ? __ENV.TENANTS.split(',')
  : Array.from({ length: parseInt(__ENV.TENANTS || '10') }, (_, i) => `tenant-${i + 1}`);

// Blueprint §16 SLO targets, codified so every scenario can opt into
// the same threshold set. Numbers are milliseconds.
export const SLI_THRESHOLDS = {
  // Generic API path — applies to every list/read/get not covered by
  // a more specific bucket below.
  api_p95:           200,
  api_p99:           500,
  // Search path (ADR 0083). Wider headroom than the generic API
  // budget because the federated search aggregates across services.
  search_p95:        300,
  search_p99:        1000,
  // Upload-initiate roundtrip (does NOT include the MinIO PUT).
  upload_init_p99:   200,
  // Full 10 MB upload (initiate + PUT + complete). §16 target.
  upload_10mb_p95:   2000,
  // RAG/Doc-Q&A (ADR 0080). Streaming-mode TTFT.
  rag_ttft_p95:      1500,
  // Signing envelope create.
  sign_create_p95:   400,
  // Read paths that hit a single Postgres row.
  doc_read_p99:      150,
  download_url_p99:  100,
  authz_p99:         5,
  autocomplete_p99:  50,
  // OCR throughput (pages/minute/worker). Validated by
  // 04-ocr-pipeline.js, not the k6 protocol.
  ocr_pages_per_min: 1000,
};

// Target VUs for a "real" §16 run. Scenarios reference this so a
// single TARGET_VUS env var rescales the whole campaign for smoke
// runs (TARGET_VUS=500) vs full runs (TARGET_VUS=100000).
export const TARGET_VUS = parseInt(__ENV.TARGET_VUS || '10000');
// Hold duration of the steady-state phase. Defaults to 1 hour per
// the RUNBOOK; smoke runs override to '5m'.
export const HOLD_DURATION = __ENV.HOLD_DURATION || '1h';
// Burst multiplier — §16 calls for 5× the sustained req/s for 10 min.
export const BURST_MULT = parseFloat(__ENV.BURST_MULT || '5');

export function randomTenant() {
  return TENANTS[Math.floor(Math.random() * TENANTS.length)];
}

export function randomString(len) {
  const chars = 'abcdefghijklmnopqrstuvwxyz0123456789';
  let s = '';
  for (let i = 0; i < len; i++) s += chars[Math.floor(Math.random() * chars.length)];
  return s;
}

export function headers(tenantId, userId) {
  return {
    'Content-Type': 'application/json',
    'X-Tenant-ID': tenantId || randomTenant(),
    'X-User-ID': userId || `user-${randomString(8)}`,
    'Authorization': `Bearer test-token-${randomString(16)}`,
  };
}

export function checkSLI(name, durationMs, thresholdMs) {
  if (durationMs > thresholdMs) {
    console.warn(`SLI BREACH: ${name} = ${durationMs}ms > ${thresholdMs}ms threshold`);
    return false;
  }
  return true;
}
