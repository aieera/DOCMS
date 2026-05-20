// Scenario 11 — Upload → Workflow → Approve (ADR 0105, §16 write path).
//
// Full write journey: create document → initiate upload → PUT bytes
// to MinIO → complete → create version → start review workflow →
// approver fetches task list → approves the task.
//
// 10 MB payload — matches the §16 "p95 upload (10 MB) < 2s" budget.
//
// SLOs validated:
//   - upload_10mb_p95 < 2000ms   (initiate + PUT + complete + version)
//   - api_p95 < 200ms / p99 < 500ms   (everything else in the chain)
//   - error_rate < 0.1%
import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Trend, Rate } from 'k6/metrics';
import exec from 'k6/execution';

import { BASE_URL, TARGET_VUS, HOLD_DURATION } from '../lib/config.js';
import { authedHeaders, ensureSessionOnce, pickTenant } from '../lib/session.js';

const createDocDur   = new Trend('sli_create_doc',          true);
const uploadInitDur  = new Trend('sli_upload_init',         true);
const uploadPutDur   = new Trend('sli_upload_put',          true);
const uploadDoneDur  = new Trend('sli_upload_complete',     true);
const versionCrtDur  = new Trend('sli_version_create',      true);
const uploadFullDur  = new Trend('sli_upload_full_10mb',    true);
const workflowStart  = new Trend('sli_workflow_start',      true);
const taskListDur    = new Trend('sli_task_list',           true);
const taskApproveDur = new Trend('sli_task_approve',        true);
const errors         = new Rate('error_rate');

// 10 MB random payload re-used across iterations. k6 ArrayBuffer is
// the cheapest way to source bytes — generating per-iteration would
// blow GC and the test would measure k6 itself.
const TEN_MB = new Uint8Array(10 * 1024 * 1024);
for (let i = 0; i < TEN_MB.length; i++) TEN_MB[i] = i & 0xff;

export const options = {
  scenarios: {
    upload_workflow_approve: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages:   [
        // Writes are heavier — ramp at ~15% of the read scenario's VU
        // count so the SUT sees the same 70/15 split implied by §16's
        // mixed-realistic weighting.
        { duration: '10m', target: Math.ceil(TARGET_VUS * 0.015) },
        { duration: '20m', target: Math.ceil(TARGET_VUS * 0.15) },
        { duration: HOLD_DURATION, target: Math.ceil(TARGET_VUS * 0.15) },
        { duration: '20m', target: 0 },
      ],
      gracefulRampDown: '30s',
    },
  },
  thresholds: {
    'sli_create_doc':       ['p(95)<200', 'p(99)<500'],
    'sli_upload_init':      ['p(99)<200'],
    'sli_upload_complete':  ['p(95)<200', 'p(99)<500'],
    'sli_version_create':   ['p(95)<200', 'p(99)<500'],
    'sli_upload_full_10mb': ['p(95)<2000'],
    'sli_workflow_start':   ['p(95)<300', 'p(99)<800'],
    'sli_task_list':        ['p(95)<200'],
    'sli_task_approve':     ['p(95)<300'],
    'error_rate':           ['rate<0.001'],
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

  // 0. Resolve a workspace + folder. We assume the seeder created
  //    "load-test-ws" per tenant with a "Root" folder; the response
  //    list endpoints are cheap enough to call once per iteration.
  let workspaceID = null;
  let folderID    = null;
  {
    const res = http.get(`${BASE_URL}/workspaces?limit=1`, { headers: h, tags: { endpoint: 'workspace_list' } });
    if (res.status !== 200) { errors.add(1); return; }
    workspaceID = (res.json()?.workspaces ?? res.json()?.items ?? [])[0]?.id;
    if (!workspaceID) return;
    const fr = http.get(`${BASE_URL}/folders?workspace_id=${workspaceID}&limit=1`,
      { headers: h, tags: { endpoint: 'folder_list' } });
    folderID = (fr.json() ?? [])[0]?.id;
  }

  const fileName = `loadtest-${vu}-${Date.now()}.pdf`;
  const mime     = 'application/pdf';
  const tStart   = Date.now();
  let docID, uploadSession, blobID;

  // 1. CreateDocument.
  group('create_doc', () => {
    const res = http.post(`${BASE_URL}/documents`,
      JSON.stringify({ workspace_id: workspaceID, folder_id: folderID, title: fileName, tags: ['loadtest'] }),
      { headers: h, tags: { endpoint: 'create_doc' } });
    createDocDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
    if (res.status >= 300) return;
    docID = res.json()?.id;
  });
  if (!docID) return;

  // 2. InitiateUpload.
  group('upload_init', () => {
    const res = http.post(`${BASE_URL}/storage/uploads/initiate`,
      JSON.stringify({
        filename:     fileName,
        mime_type:    mime,
        size_bytes:   TEN_MB.length,
        workspace_id: workspaceID,
        folder_id:    folderID,
      }),
      { headers: h, tags: { endpoint: 'upload_init' } });
    uploadInitDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
    if (res.status === 200) uploadSession = res.json();
  });
  if (!uploadSession?.presigned_put_url) return;

  // 3. PUT bytes — MinIO/S3, bypasses Kong. No auth headers needed,
  //    the presigned URL embeds them.
  group('upload_put', () => {
    const res = http.put(uploadSession.presigned_put_url, TEN_MB.buffer, {
      headers: { 'Content-Type': mime },
      tags:    { endpoint: 'upload_put' },
    });
    uploadPutDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
  });

  // 4. CompleteUpload — server-side virus scan + blob row.
  group('upload_complete', () => {
    const res = http.post(`${BASE_URL}/storage/uploads/${uploadSession.upload_id}/complete`,
      '{}',
      { headers: h, tags: { endpoint: 'upload_complete' } });
    uploadDoneDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
    if (res.status === 200) {
      const body = res.json();
      blobID = body?.content_blob_id ?? uploadSession.content_blob_id ?? uploadSession.existing_blob_id;
    }
  });
  if (!blobID) return;

  // 5. CreateVersion — link blob → document row.
  group('version_create', () => {
    const res = http.post(`${BASE_URL}/documents/${docID}/versions`,
      JSON.stringify({ content_blob_id: blobID, change_summary: 'load test initial' }),
      { headers: h, tags: { endpoint: 'version_create' } });
    versionCrtDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
  });

  uploadFullDur.add(Date.now() - tStart);

  sleep(0.5 + Math.random() * 1.0);

  // 6. Start a review workflow on the new doc.
  let workflowID = null;
  group('workflow_start', () => {
    const res = http.post(`${BASE_URL}/workflows/instances`,
      JSON.stringify({
        workflow_type: 'document_review',
        document_id:   docID,
        assignees:     [`load+${tenantID}+0@vaultdms.test`],
      }),
      { headers: h, tags: { endpoint: 'workflow_start' } });
    workflowStart.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
    if (res.status >= 200 && res.status < 300) workflowID = res.json()?.id;
  });

  sleep(0.5 + Math.random() * 1.0);

  // 7. Approver lists their tasks.
  let taskID = null;
  group('task_list', () => {
    const res = http.get(`${BASE_URL}/tasks/mine?status=pending&limit=20`,
      { headers: h, tags: { endpoint: 'task_list' } });
    taskListDur.add(res.timings.duration);
    errors.add(res.status >= 500 ? 1 : 0);
    if (res.status === 200) {
      const tasks = res.json()?.tasks ?? res.json()?.items ?? [];
      // Match task to this VU's doc when possible, else take the head.
      const match = tasks.find((t) => t.document_id === docID) || tasks[0];
      if (match) taskID = match.id;
    }
  });

  // 8. Approve.
  if (taskID) {
    group('task_approve', () => {
      const res = http.post(`${BASE_URL}/tasks/${taskID}/decision`,
        JSON.stringify({ decision: 'approve', comment: 'load test' }),
        { headers: h, tags: { endpoint: 'task_approve' } });
      taskApproveDur.add(res.timings.duration);
      errors.add(res.status >= 500 ? 1 : 0);
    });
  }

  sleep(1.0 + Math.random() * 2.0);
}
