// Message dispatcher for the authenticated WS protocol.
//
// Connection is already authenticated by the HTTP-upgrade handler
// (index.js). This file only sees authenticated sockets with
// `ws.userId`, `ws.tenantId`, `ws.displayName`, `ws.cookie` already
// populated.
//
// Every inbound frame is parsed against the ClientMessage schema.
// Parse failure closes with 4400; invalid type is a no-op (already
// excluded by the discriminated union).
//
// Broadcast naming (per brief):
//   comment.created | comment.updated | comment.deleted
//   presence.joined | presence.left
//   typing.start    | typing.stop
//
// Broadcasts go through Redis pub/sub (`dms:{tenant}:doc:{doc}`) so
// multiple collaboration pods see each other's events.

import { canReadDocument } from './policy.js';
import {
  ClientMessage,
  CLOSE_FORBIDDEN,
  CLOSE_INVALID_MESSAGE,
} from './schemas.js';

function channelFor(tenantId, docId) {
  return `dms:${tenantId}:doc:${docId}`;
}

async function broadcast(redisPub, connections, tenantId, docId, payload, senderUserId) {
  const wire = { ...payload, _senderUserId: senderUserId };
  await redisPub.publish(channelFor(tenantId, docId), JSON.stringify(wire));
  connections.broadcastToDoc(tenantId, docId, wire, senderUserId);
}

export async function handleMessage(ws, raw, connections, redisPub) {
  let frame;
  try { frame = JSON.parse(raw.toString()); } catch {
    ws.close(CLOSE_INVALID_MESSAGE, 'invalid json');
    return;
  }
  const parsed = ClientMessage.safeParse(frame);
  if (!parsed.success) {
    ws.close(CLOSE_INVALID_MESSAGE, 'schema violation');
    return;
  }
  const msg = parsed.data;
  const now = new Date().toISOString();

  switch (msg.type) {
    case 'room.join': {
      const allowed = await canReadDocument(ws.cookie, msg.document_id);
      if (!allowed) {
        ws.close(CLOSE_FORBIDDEN, 'document:read denied');
        return;
      }
      connections.subscribeToDoc(ws, msg.document_id);
      await broadcast(redisPub, connections, ws.tenantId, msg.document_id, {
        type: 'presence.joined',
        document_id: msg.document_id,
        user_id: ws.userId,
        display_name: ws.displayName,
        at: now,
      }, ws.userId);
      return;
    }

    case 'room.leave': {
      connections.unsubscribeFromDoc(ws, msg.document_id);
      await broadcast(redisPub, connections, ws.tenantId, msg.document_id, {
        type: 'presence.left',
        document_id: msg.document_id,
        user_id: ws.userId,
        at: now,
      }, ws.userId);
      return;
    }

    case 'comment.create': {
      // Comment persistence lives in the document service's REST API.
      // This broadcast is the realtime fan-out ONLY; clients still POST
      // the comment to /documents/:id/comments to persist. Backend
      // could publish the authoritative event through NATS instead,
      // but the brief scopes this service to broadcast-only.
      await broadcast(redisPub, connections, ws.tenantId, msg.document_id, {
        type: 'comment.created',
        document_id: msg.document_id,
        body: msg.body,
        parent_id: msg.parent_id ?? null,
        user_id: ws.userId,
        display_name: ws.displayName,
        created_at: now,
      }, ws.userId);
      return;
    }

    case 'comment.update': {
      await broadcast(redisPub, connections, ws.tenantId, msg.document_id, {
        type: 'comment.updated',
        document_id: msg.document_id,
        comment_id: msg.comment_id,
        body: msg.body,
        user_id: ws.userId,
        updated_at: now,
      }, ws.userId);
      return;
    }

    case 'comment.delete': {
      await broadcast(redisPub, connections, ws.tenantId, msg.document_id, {
        type: 'comment.deleted',
        document_id: msg.document_id,
        comment_id: msg.comment_id,
        user_id: ws.userId,
        deleted_at: now,
      }, ws.userId);
      return;
    }

    case 'typing.start': {
      await broadcast(redisPub, connections, ws.tenantId, msg.document_id, {
        type: 'typing.start',
        document_id: msg.document_id,
        user_id: ws.userId,
        display_name: ws.displayName,
      }, ws.userId);
      return;
    }

    case 'typing.stop': {
      await broadcast(redisPub, connections, ws.tenantId, msg.document_id, {
        type: 'typing.stop',
        document_id: msg.document_id,
        user_id: ws.userId,
      }, ws.userId);
      return;
    }
  }
}
