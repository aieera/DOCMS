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

import {
  createComment,
  updateComment,
  deleteComment,
  listComments,
} from './comments-api.js';

// Read per call so the test harness can point at a fixture server.
function authServiceURL() {
  return process.env.AUTH_SERVICE_URL || 'http://localhost:8080';
}

async function validateToken(token) {
  try {
    const resp = await fetch(`${authServiceURL()}/api/v1/auth/me`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!resp.ok) return null;
    const user = await resp.json();
    return { userId: user.id, tenantId: user.tenant_id, displayName: user.display_name };
  } catch {
    return null;
  }
}

/**
 * Publish to the doc room: the caller gets `data` directly (their
 * confirmation carries the persisted ids), every other subscriber —
 * local and cross-instance via Redis — gets it through the broadcast
 * path, which excludes the sender.
 */
async function publishToRoom(ws, connections, redisPub, docId, data) {
  const channel = `dms:${ws.tenantId}:doc:${docId}`;
  await redisPub.publish(channel, JSON.stringify(data));
  connections.broadcastToDoc(ws.tenantId, docId, data, ws.userId);
  if (ws.readyState === 1) ws.send(JSON.stringify(data));
}

function sendError(ws, op, message) {
  if (ws.readyState === 1) {
    ws.send(JSON.stringify({ type: 'comment_error', op, error: message }));
  }
}

export async function handleMessage(ws, msg, connections, redisPub, authTimeout) {
  switch (msg.type) {
    case 'auth': {
      const user = await validateToken(msg.token);
      if (!user) {
        ws.close(4001, 'invalid token');
        return;
      }
      clearTimeout(authTimeout);
      ws.authenticated = true;
      ws.tenantId = user.tenantId;
      ws.userId = user.userId;
      ws.displayName = user.displayName;
      // Retained for document-service calls: forwarding the USER's own
      // token means comment ACL is enforced by the policy engine per
      // acting user, not by a service account.
      ws.token = msg.token;
      await connections.addConnection(ws);
      ws.send(JSON.stringify({ type: 'auth_ok', user_id: user.userId }));
      break;
    }

    case 'subscribe': {
      if (!ws.authenticated) return;
      const docId = msg.doc_id;
      if (!docId) return;
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
        const comments = await listComments(ws.token, docId);
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
      const data = {
        type: 'presence_update',
        doc_id: msg.doc_id,
        user_id: ws.userId,
        display_name: ws.displayName,
        page: msg.page,
        cursor: msg.cursor,
        _senderUserId: ws.userId,
      };
      // Publish to Redis for cross-instance delivery.
      const channel = `dms:${ws.tenantId}:doc:${msg.doc_id}`;
      await redisPub.publish(channel, JSON.stringify(data));
      // Local broadcast (same instance).
      connections.broadcastToDoc(ws.tenantId, msg.doc_id, data, ws.userId);
      break;
    }

    case 'comment_create': {
      if (!ws.authenticated) return;
      if (!msg.doc_id || !msg.body) {
        sendError(ws, 'create', 'doc_id and body are required');
        return;
      }
      let persisted;
      try {
        persisted = await createComment(ws.token, msg.doc_id, msg.body, msg.parent_id || null);
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
      let persisted;
      try {
        persisted = await updateComment(ws.token, msg.comment_id, msg.body);
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
      try {
        await deleteComment(ws.token, msg.comment_id);
      } catch (err) {
        console.error(`comment delete failed id=${msg.comment_id}: ${err.message}`);
        sendError(ws, 'delete', 'comment delete was not saved');
        return;
      }
      await publishToRoom(ws, connections, redisPub, msg.doc_id, {
        type: 'comment_deleted',
        doc_id: msg.doc_id,
        comment_id: msg.comment_id,
        user_id: ws.userId,
        _senderUserId: ws.userId,
      });
      break;
    }

    default:
      break;
  }
}
