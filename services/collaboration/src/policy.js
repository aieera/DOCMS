// Policy-service client. Thin wrapper for the `document:read` check
// invoked on every room.join. Forwards the user's session cookie so
// the policy service resolves the subject from auth middleware.
//
// Policy HTTP contract (services/policy/internal/handler/http.go:89):
//   POST /api/v1/permissions/check
//   { resource_type: "document", resource_id: <uuid>, action: "read" }
//   → { allowed: bool, reason: string }

const POLICY_SERVICE_URL = process.env.POLICY_SERVICE_URL || 'http://localhost:8081';

/**
 * Returns `true` iff the policy service allows `subject` to `read`
 * `documentId`. Any transport error or non-2xx response is treated
 * as DENY (fail-closed).
 */
export async function canReadDocument(cookie, documentId) {
  try {
    const resp = await fetch(`${POLICY_SERVICE_URL}/api/v1/permissions/check`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Cookie: cookie,
      },
      body: JSON.stringify({
        resource_type: 'document',
        resource_id: documentId,
        action: 'read',
      }),
    });
    if (!resp.ok) return false;
    const body = await resp.json();
    return body?.allowed === true;
  } catch {
    return false;
  }
}
