/**
 * Message handler. Processes client→server messages:
 *   auth, subscribe, unsubscribe, presence,
 *   comment_create, comment_update, comment_delete
 *
 * Comment mutations PERSIST through the document service (see
 * comments-api.js) before they broadcast — the old comment_create was
 * broadcast-only, so a live comment existed exactly until the last
 * client refetched, then silently vanished (and update/delete had no
 * handlers at all). Persist-then-broadcast means the live event and the
 * stored row can't desync; a persistence failure sends a comment_error
 * frame to the caller and broadcasts NOTHING.
 *
 * Comments are NOT part of the Yjs CRDT — Yjs carries the note body;
 * comments live in the document service's Postgres store (the same one
 * the REST CommentsPanel uses). "Room load" hydration happens on
 * subscribe via a comments_snapshot frame.
 */

import { gatewayHeaders } from './gateway-sig.js';
import { fetchDocPermissions } from './doc-permissions.js';
import { isAllowedOrigin } from './origin.js';
import {
  createComment,
  updateComment,
  deleteComment,
  listComments,
} from './comments-api.js';

// Read per call so the test harness can point at a fixture server.
function authServiceURL() {
  // Same default as yjs-server.js: the in-cluster auth service.
  return process.env.AUTH_SERVICE_URL || 'http://auth:8080';
}

/**
 * Resolve the acting user from auth /me with the given credential headers
 * (Bearer token or session Cookie). The auth service sits behind
 * RequireGatewaySignature: without the signature it answers 401 for
 * every caller (CB-6). Returns { user } or { code } where code is 4401
 * (credentials rejected) or 4503 (auth unavailable).
 */
async function resolveSession(credHeaders) {
  let resp;
  try {
    resp = await fetch(`${authServiceURL()}/api/v1/auth/me`, {
      headers: { ...credHeaders, ...gatewayHeaders() },
    });
  } catch {
    return { code: 4503 };
  }
  if (resp.status === 401 || resp.status === 403) return { code: 4401 };
  if (!resp.ok) return { code: 4503 };
  let body;
  try {
    body = await resp.json();
  } catch {
    return { code: 4503 };
  }
  const userId = body.id || body.user_id;
  const tenantId = body.tenant_id || body.tenantId;
  if (!userId || !tenantId) return { code: 4401 };
  return { user: { userId, tenantId, displayName: body.display_name } };
}

/**
 * The credential headers backend calls are made with on behalf of this
 * socket: the upgrade's Cookie header in cookie mode (CB-3), else the
 * Bearer token from the auth frame.
 */
function authHeaders(ws) {
  return ws.authMode === 'cookie'
    ? { Cookie: ws.sessionCookie }
    : { Authorization: `Bearer ${ws.token}` };
}

/** Close code for a session that is (or became) invalid, per auth mode. */
function sessionLostCode(ws) {
  return ws.authMode === 'cookie' ? 4401 : 4001;
}

/**
 * Doc-level RBAC (§8) gate for room subscription. A valid tenant session
 * is necessary but NOT sufficient — without this check any authenticated
 * tenant user could `subscribe` to any doc_id and receive all FUTURE live
 * comment bodies + collaborator presence for it.
 *
 * Asks the document service's permission summary (CB-3) — the same
 * authority yjs-server.js uses — with the acting user's own credentials, and
 * FAILS CLOSED: any deny, auth failure, or transport error returns false.
 */
async function checkDocView(ws, docId) {
  try {
    const perms = await fetchDocPermissions(authHeaders(ws), docId);
    return perms.view === true;
  } catch {
    return false;
  }
}

// CB-3: how often a /ws socket's document subscriptions are re-authorized.
export const WS_RECHECK_MS = 60_000;

/**
 * CB-3: re-read `view` for every document this socket subscribed to.
 * Lost view (or an unavailable permission source — fail closed) removes
 * the subscription and sends `subscription_revoked`; an invalid session
 * closes the socket (4401 for a cookie session, 4001 for a token session).
 * A cookie session is re-checked with the cookie captured at the upgrade,
 * exactly as /yjs does.
 */
export async function recheckSubscriptions(ws, connections) {
  if (!ws.authenticated || ws.readyState !== 1 || !(ws.subscribedDocs instanceof Set)) return;
  for (const docId of [...ws.subscribedDocs]) {
    let perms;
    try {
      perms = await fetchDocPermissions(authHeaders(ws), docId);
    } catch {
      perms = { code: 4503 };
    }
    if (ws.readyState !== 1) return;
    if (perms.code === 4401) {
      ws.close(sessionLostCode(ws), 'session expired');
      return;
    }
    if (perms.code || !perms.view) {
      connections.unsubscribeFromDoc(ws, docId);
      ws.send(JSON.stringify({
        type: 'subscription_revoked',
        doc_id: docId,
        error: perms.code ? 'permission check unavailable' : 'no access to document',
      }));
    }
  }
}

/** Run recheckSubscriptions every `intervalMs` until the socket closes. */
export function startSubscriptionRecheck(ws, connections, intervalMs = WS_RECHECK_MS) {
  let inFlight = false;
  const timer = setInterval(async () => {
    if (inFlight) return;
    inFlight = true;
    try {
      await recheckSubscriptions(ws, connections);
    } catch (err) {
      console.error('ws: subscription re-check failed', err);
    } finally {
      inFlight = false;
    }
  }, intervalMs);
  timer.unref?.();
  ws.once('close', () => clearInterval(timer));
  return () => clearInterval(timer);
}

/**
 * Publish to the doc room: the caller gets `data` directly (their
 * confirmation carries the persisted ids), every other subscriber —
 * local and cross-instance via Redis — gets it through the broadcast
 * path, which excludes the sender.
 */
async function publishToRoom(ws, connections, redisPub, docId, data) {
  if (!belongsTo(data.comment, docId)) {
    // Persisted (the document service authorized it), but it belongs to
    // another document: confirm to the sender only.
    if (ws.readyState === 1) ws.send(JSON.stringify(data));
    return;
  }
  const channel = `dms:${ws.tenantId}:doc:${docId}`;
  // Publish ONCE to Redis. Every instance subscribed to this channel —
  // including this one — receives the message on redisSub and fans it
  // out to its local subscribers there (index.js), excluding
  // data._senderUserId. Broadcasting locally here as well delivered the
  // event twice to every other local subscriber; the loopback is the
  // single delivery path (same pattern the annotation NATS bridge uses).
  await redisPub.publish(channel, JSON.stringify(data));
  // The sender is excluded from the loopback (by _senderUserId), so send
  // their own confirmation directly — it carries the persisted ids.
  if (ws.readyState === 1) ws.send(JSON.stringify(data));
}

// CB-7 limits. A /ws frame may only act on a document the socket has
// subscribed to (subscribe is where the view check runs), and every
// client-supplied field is bounded before it is persisted or fanned out.
export const MAX_COMMENT_BODY_BYTES = 16 * 1024;
export const MAX_PRESENCE_FIELD_BYTES = 1024;
export const MAX_ID_LENGTH = 128;
export const MAX_SUBSCRIPTIONS_PER_SOCKET = 32;

function isId(v) {
  return typeof v === 'string' && v.length > 0 && v.length <= MAX_ID_LENGTH;
}

function isSubscribed(ws, docId) {
  return isId(docId) && ws.subscribedDocs instanceof Set && ws.subscribedDocs.has(docId);
}

function fitsBytes(value, max) {
  if (value === undefined) return true;
  try {
    const s = JSON.stringify(value);
    return s === undefined || Buffer.byteLength(s, 'utf8') <= max;
  } catch {
    return false;
  }
}

function validBody(body) {
  return typeof body === 'string' && body.length > 0
    && Buffer.byteLength(body, 'utf8') <= MAX_COMMENT_BODY_BYTES;
}

// A persisted comment that names a different document must not be
// broadcast into msg.doc_id's room: that would hand another document's
// comment body to viewers who were only authorized for this one.
function belongsTo(persisted, docId) {
  const owner = persisted && (persisted.document_id || persisted.documentId);
  return !owner || owner === docId;
}

// True only when `commentId` is listed among `docId`'s comments. Any
// failure to list counts as "not shown" (no room broadcast).
async function commentInDoc(auth, docId, commentId) {
  try {
    const list = await listComments(auth, docId);
    return Array.isArray(list) && list.some((c) => c && c.id === commentId
      && (!(c.document_id || c.documentId) || (c.document_id || c.documentId) === docId));
  } catch {
    return false;
  }
}

function sendError(ws, op, message) {
  if (ws.readyState === 1) {
    ws.send(JSON.stringify({ type: 'comment_error', op, error: message }));
  }
}

/**
 * CB-3: authenticate a /ws socket from one auth frame. Two ways in:
 * cookie mode (browsers) and Bearer token (non-browser clients). On
 * failure the socket is closed and no identity is registered.
 */
async function authenticate(ws, msg, connections, authTimeout) {
  // Two ways in:
  //   {type:'auth', mode:'cookie'} — browsers. The identity comes ONLY
  //     from the HttpOnly dms_session cookie on the gateway-signed
  //     upgrade (same as /yjs); any token/user/tenant field in the
  //     frame is ignored, so page JS never holds the session secret.
  //   {type:'auth', token} — non-browser clients (extension, mobile,
  //     add-ins): Bearer token, as before.
  let user;
  if (msg.mode === 'cookie') {
    // CB-6 belt and braces: verifyUpgrade already refused a cookie-carrying
    // upgrade from a foreign Origin; never accept cookie mode without one.
    if (!isAllowedOrigin(ws.upgradeOrigin)) {
      ws.close(4403, 'origin not allowed');
      return;
    }
    const cookie = typeof ws.upgradeCookie === 'string' ? ws.upgradeCookie : '';
    if (!/(?:^|;\s*)dms_session=[^;\s]/.test(cookie)) {
      ws.close(4401, 'auth required');
      return;
    }
    const res = await resolveSession({ Cookie: cookie });
    if (!res.user) {
      ws.close(res.code, res.code === 4401 ? 'auth required' : 'auth service unavailable');
      return;
    }
    user = res.user;
    ws.authMode = 'cookie';
    // Retained for document-service calls and the 60 s re-check.
    ws.sessionCookie = cookie;
  } else {
    const token = msg.token;
    const res = typeof token === 'string' && token !== ''
      ? await resolveSession({ Authorization: `Bearer ${token}` })
      : { code: 4401 };
    if (!res.user) {
      ws.close(4001, 'invalid token');
      return;
    }
    user = res.user;
    ws.authMode = 'token';
    // Retained for document-service calls: forwarding the USER's own
    // token means comment ACL is enforced by the policy engine per
    // acting user, not by a service account.
    ws.token = token;
  }
  if (ws.readyState !== 1) return;
  clearTimeout(authTimeout);
  ws.authenticated = true;
  ws.tenantId = user.tenantId;
  ws.userId = user.userId;
  ws.displayName = user.displayName;
  await connections.addConnection(ws);
  ws.send(JSON.stringify({ type: 'auth_ok', user_id: user.userId }));
}

export async function handleMessage(ws, msg, connections, redisPub, authTimeout) {
  switch (msg.type) {
    case 'auth': {
      // A socket authenticates at most once. The claim is taken
      // synchronously, before any await: frames are handled concurrently,
      // and two back-to-back auth frames would otherwise both resolve and
      // register two identities (the first left `online` forever).
      if (ws.authenticated || ws.authenticating) return;
      ws.authenticating = true;
      try {
        await authenticate(ws, msg, connections, authTimeout);
      } finally {
        ws.authenticating = false;
      }
      break;
    }

    case 'subscribe': {
      if (!ws.authenticated) return;
      const docId = msg.doc_id;
      if (!isId(docId)) return;
      // Count in-flight subscribes too: frames are handled concurrently,
      // so checking subscribedDocs alone lets a burst exceed the cap.
      const pending = ws.pendingSubscribes || 0;
      if (!ws.subscribedDocs.has(docId) && ws.subscribedDocs.size + pending >= MAX_SUBSCRIPTIONS_PER_SOCKET) {
        if (ws.readyState === 1) {
          ws.send(JSON.stringify({ type: 'subscribe_denied', doc_id: docId, error: 'too many subscriptions' }));
        }
        return;
      }
      // Enforce doc-level `view` BEFORE joining the room — otherwise the
      // subscriber receives every future live comment body + presence
      // event for a document they may have no access to.
      ws.pendingSubscribes = pending + 1;
      let allowed;
      try {
        allowed = await checkDocView(ws, docId);
      } finally {
        ws.pendingSubscribes -= 1;
      }
      if (!allowed) {
        if (ws.readyState === 1) {
          ws.send(JSON.stringify({ type: 'subscribe_denied', doc_id: docId, error: 'no access to document' }));
        }
        return;
      }
      // The socket may have closed while the check was in flight; joining
      // now would leave a dead socket in the room.
      if (ws.readyState !== 1) return;
      connections.subscribeToDoc(ws, docId);
      const users = connections.getDocPresence(ws.tenantId, docId);
      connections.broadcastToDoc(ws.tenantId, docId, {
        type: 'presence_update', doc_id: docId, users,
      });
      // Room-load reconcile: hydrate the persisted comments so a late
      // joiner sees updates/deletes that happened before they arrived.
      // Best-effort — presence/CRDT must not depend on the comment
      // store being reachable.
      try {
        const comments = await listComments(authHeaders(ws), docId);
        if (ws.readyState === 1) {
          ws.send(JSON.stringify({
            type: 'comments_snapshot', doc_id: docId, comments: comments || [],
          }));
        }
      } catch (err) {
        console.error(`comments hydrate failed doc=${docId}: ${err.message}`);
      }
      break;
    }

    case 'unsubscribe': {
      if (!ws.authenticated) return;
      connections.unsubscribeFromDoc(ws, msg.doc_id);
      break;
    }

    case 'presence': {
      if (!ws.authenticated) return;
      if (!isSubscribed(ws, msg.doc_id)) return;
      if (!fitsBytes(msg.page, MAX_PRESENCE_FIELD_BYTES) || !fitsBytes(msg.cursor, MAX_PRESENCE_FIELD_BYTES)) return;
      const data = {
        type: 'presence_update',
        doc_id: msg.doc_id,
        user_id: ws.userId,
        display_name: ws.displayName,
        page: msg.page,
        cursor: msg.cursor,
        _senderUserId: ws.userId,
      };
      // Publish once to Redis; the redisSub loopback (index.js) delivers
      // to local subscribers too, excluding the sender via _senderUserId.
      // A direct local broadcast here would double-deliver to every other
      // local subscriber.
      const channel = `dms:${ws.tenantId}:doc:${msg.doc_id}`;
      await redisPub.publish(channel, JSON.stringify(data));
      break;
    }

    case 'comment_create': {
      if (!ws.authenticated) return;
      if (!msg.doc_id || !msg.body) {
        sendError(ws, 'create', 'doc_id and body are required');
        return;
      }
      if (!isSubscribed(ws, msg.doc_id)) {
        sendError(ws, 'create', 'not subscribed to document');
        return;
      }
      if (!validBody(msg.body) || (msg.parent_id != null && !isId(msg.parent_id))) {
        sendError(ws, 'create', 'comment too large or malformed');
        return;
      }
      let persisted;
      try {
        persisted = await createComment(authHeaders(ws), msg.doc_id, msg.body, msg.parent_id || null);
      } catch (err) {
        console.error(`comment create failed doc=${msg.doc_id}: ${err.message}`);
        sendError(ws, 'create', 'comment was not saved');
        return;
      }
      await publishToRoom(ws, connections, redisPub, msg.doc_id, {
        type: 'comment_added',
        doc_id: msg.doc_id,
        comment: persisted,
        // Legacy flat fields kept for older consumers of comment_added.
        body: msg.body,
        parent_id: msg.parent_id || null,
        user_id: ws.userId,
        display_name: ws.displayName,
        created_at: persisted?.created_at || new Date().toISOString(),
        _senderUserId: ws.userId,
      });
      break;
    }

    case 'comment_update': {
      if (!ws.authenticated) return;
      if (!msg.doc_id || !msg.comment_id || !msg.body) {
        sendError(ws, 'update', 'doc_id, comment_id and body are required');
        return;
      }
      if (!isSubscribed(ws, msg.doc_id)) {
        sendError(ws, 'update', 'not subscribed to document');
        return;
      }
      if (!validBody(msg.body) || !isId(msg.comment_id)) {
        sendError(ws, 'update', 'comment too large or malformed');
        return;
      }
      let persisted;
      try {
        persisted = await updateComment(authHeaders(ws), msg.comment_id, msg.body);
      } catch (err) {
        console.error(`comment update failed id=${msg.comment_id}: ${err.message}`);
        sendError(ws, 'update', 'comment update was not saved');
        return;
      }
      await publishToRoom(ws, connections, redisPub, msg.doc_id, {
        type: 'comment_updated',
        doc_id: msg.doc_id,
        comment_id: msg.comment_id,
        comment: persisted,
        body: msg.body,
        user_id: ws.userId,
        _senderUserId: ws.userId,
      });
      break;
    }

    case 'comment_delete': {
      if (!ws.authenticated) return;
      if (!msg.doc_id || !msg.comment_id) {
        sendError(ws, 'delete', 'doc_id and comment_id are required');
        return;
      }
      if (!isSubscribed(ws, msg.doc_id)) {
        sendError(ws, 'delete', 'not subscribed to document');
        return;
      }
      if (!isId(msg.comment_id)) {
        sendError(ws, 'delete', 'malformed comment_id');
        return;
      }
      // DELETE returns no body, so prove before deleting that the comment
      // belongs to msg.doc_id (the document service has no GET for one
      // comment; its per-document list carries every comment's id).
      const inRoom = await commentInDoc(authHeaders(ws), msg.doc_id, msg.comment_id);
      try {
        await deleteComment(authHeaders(ws), msg.comment_id);
      } catch (err) {
        console.error(`comment delete failed id=${msg.comment_id}: ${err.message}`);
        sendError(ws, 'delete', 'comment delete was not saved');
        return;
      }
      const deleted = {
        type: 'comment_deleted',
        doc_id: msg.doc_id,
        comment_id: msg.comment_id,
        user_id: ws.userId,
        _senderUserId: ws.userId,
      };
      if (inRoom) {
        await publishToRoom(ws, connections, redisPub, msg.doc_id, deleted);
      } else if (ws.readyState === 1) {
        // Ownership could not be shown: confirm to the sender only.
        ws.send(JSON.stringify(deleted));
      }
      break;
    }

    default:
      break;
  }
}
