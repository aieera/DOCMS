/**
 * Document-service comments REST client.
 *
 * WS comment mutations persist THROUGH the document service — the same
 * store the CommentsPanel REST path writes — never a parallel store.
 * That buys, for free: Postgres persistence (FORCE RLS), the policy ACL
 * check on the acting user, and the dms.comment.{created,updated,
 * deleted}.v1 outbox events every other consumer already relies on.
 *
 * Auth: the caller's own session token rides as Bearer (the document
 * service resolves + authorizes the real user — "validate session +
 * tenant + doc ACL" happens there, where the policy engine lives).
 * X-Gateway-Signature is the in-cluster call convention (same as the
 * connector's ingest client); empty secret omits the header for dev
 * stacks that run without the gateway posture.
 *
 * Env is read per call (not module load) so the test harness can point
 * at a fixture server.
 */

function baseURL() {
  return (
    process.env.DOCUMENT_SERVICE_URL ||
    process.env.SEDOC_DOCUMENT_URL ||
    'http://document:8080'
  );
}

async function call(token, method, path, body) {
  const headers = { Authorization: `Bearer ${token}` };
  const secret = process.env.SEDOC_GATEWAY_SECRET || '';
  if (secret) headers['X-Gateway-Signature'] = secret;
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const resp = await fetch(`${baseURL()}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!resp.ok) {
    const text = await resp.text().catch(() => '');
    throw new Error(`document service ${method} ${path}: ${resp.status} ${text.slice(0, 200)}`);
  }
  if (resp.status === 204) return null;
  return resp.json().catch(() => null);
}

/** POST /documents/{id}/comments — or the replies endpoint for threads. */
export function createComment(token, docId, body, parentId) {
  if (parentId) {
    return call(token, 'POST', `/api/v1/comments/${encodeURIComponent(parentId)}/replies`, { body });
  }
  return call(token, 'POST', `/api/v1/documents/${encodeURIComponent(docId)}/comments`, { body });
}

/** PATCH /comments/{cid} */
export function updateComment(token, commentId, body) {
  return call(token, 'PATCH', `/api/v1/comments/${encodeURIComponent(commentId)}`, { body });
}

/** DELETE /comments/{cid} */
export function deleteComment(token, commentId) {
  return call(token, 'DELETE', `/api/v1/comments/${encodeURIComponent(commentId)}`);
}

/** GET /documents/{id}/comments — room-load hydration for late joiners. */
export function listComments(token, docId) {
  return call(token, 'GET', `/api/v1/documents/${encodeURIComponent(docId)}/comments?include_resolved=true`);
}
