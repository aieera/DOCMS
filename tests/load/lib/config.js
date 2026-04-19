// Shared config and helpers for all k6 load test scenarios.
export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080/api/v1';
export const WS_URL = __ENV.WS_URL || 'ws://localhost:8083/ws';

export const TENANTS = (__ENV.TENANTS || '10').split(',').length > 1
  ? __ENV.TENANTS.split(',')
  : Array.from({ length: parseInt(__ENV.TENANTS || '10') }, (_, i) => `tenant-${i + 1}`);

export const SLI_THRESHOLDS = {
  search_p99: 300,       // ms
  upload_init_p99: 200,
  ocr_p95: 30000,
  download_url_p99: 100,
  doc_read_p99: 150,
  authz_p99: 5,
  autocomplete_p99: 50,
};

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
