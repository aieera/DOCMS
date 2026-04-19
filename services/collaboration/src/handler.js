/**
 * Message handler. Processes client→server messages:
 *   auth, subscribe, unsubscribe, presence, comment_create
 */

const AUTH_SERVICE_URL = process.env.AUTH_SERVICE_URL || 'http://localhost:8080';

async function validateToken(token) {
  try {
    const resp = await fetch(`${AUTH_SERVICE_URL}/api/v1/auth/me`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!resp.ok) return null;
    const user = await resp.json();
    return { userId: user.id, tenantId: user.tenant_id, displayName: user.display_name };
  } catch {
    return null;
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
      const comment = {
        type: 'comment_added',
        doc_id: msg.doc_id,
        body: msg.body,
        parent_id: msg.parent_id || null,
        user_id: ws.userId,
        display_name: ws.displayName,
        created_at: new Date().toISOString(),
        _senderUserId: ws.userId,
      };
      const channel = `dms:${ws.tenantId}:doc:${msg.doc_id}`;
      await redisPub.publish(channel, JSON.stringify(comment));
      connections.broadcastToDoc(ws.tenantId, msg.doc_id, comment, ws.userId);
      break;
    }

    default:
      break;
  }
}
