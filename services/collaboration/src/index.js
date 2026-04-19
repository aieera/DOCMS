import { randomUUID } from 'crypto';
import { WebSocketServer } from 'ws';
import { createClient } from './redis.js';
import { ConnectionManager } from './connections.js';
import { handleMessage } from './handler.js';
import { startAnnotationBridge } from './nats-bridge.js';
import { attachYjs } from './yjs-server.js';

const PORT = parseInt(process.env.WS_PORT || '8083', 10);
const HEARTBEAT_INTERVAL = 30_000;
const PONG_TIMEOUT = 10_000;
const NATS_URL = process.env.VAULTDMS_NATS_URL || process.env.NATS_URL || 'nats://nats:4222';

const redisSub = createClient();
const redisPub = createClient();
const redisStore = createClient();
const connections = new ConnectionManager(redisStore);

// §17.3 / D10 WS fan-out — bridge NATS annotation events into the
// Redis doc-room broadcast path. Non-fatal if it fails; WS service
// still serves connections even without the bridge.
startAnnotationBridge(redisPub, NATS_URL, randomUUID())
  .then(() => console.log(`annotation bridge subscribed to NATS ${NATS_URL}`))
  .catch((err) => console.error('annotation bridge failed to start:', err));

// Single port, two paths: existing auth/doc-room WS on /ws, Yjs CRDT
// on /yjs/{tenantId}/{docId}. Omitting the `path` option lets a
// single WebSocketServer accept either prefix and dispatch below.
const wss = new WebSocketServer({ port: PORT });

// §17.4 / E7 — Yjs path. Must be attached BEFORE the /ws handler so
// it sees the connection first and marks it routed.
attachYjs(wss);

wss.on('connection', (ws, req) => {
  // Yjs path was claimed by attachYjs; skip the existing /ws handler.
  if (ws.__routed === 'yjs') return;
  // Only the /ws path is allowed for the auth/doc-room protocol;
  // everything else was either Yjs (handled above) or unknown.
  if (req.url !== '/ws' && !(req.url || '').startsWith('/ws?')) {
    ws.close(1008, 'unknown path');
    return;
  }

  ws.isAlive = true;
  ws.authenticated = false;
  ws.tenantId = null;
  ws.userId = null;
  ws.subscribedDocs = new Set();

  const authTimeout = setTimeout(() => {
    if (!ws.authenticated) ws.close(4001, 'auth timeout');
  }, 10_000);

  ws.on('pong', () => { ws.isAlive = true; });

  ws.on('message', async (raw) => {
    let msg;
    try { msg = JSON.parse(raw.toString()); } catch { return; }
    await handleMessage(ws, msg, connections, redisPub, authTimeout);
  });

  ws.on('close', async () => {
    clearTimeout(authTimeout);
    if (ws.tenantId && ws.userId) {
      await connections.removeConnection(ws);
    }
  });
});

// Heartbeat: ping every 30s, close if no pong within 10s.
setInterval(() => {
  wss.clients.forEach((ws) => {
    if (!ws.isAlive) return ws.terminate();
    ws.isAlive = false;
    ws.ping();
  });
}, HEARTBEAT_INTERVAL);

// Redis subscriber for cross-instance message forwarding.
redisSub.on('message', (channel, message) => {
  // Channel format: dms:{tenantId}:doc:{docId}
  const parts = channel.split(':');
  if (parts.length < 4) return;
  const tenantId = parts[1];
  const docId = parts[3];
  const data = JSON.parse(message);
  connections.broadcastToDoc(tenantId, docId, data, data._senderUserId);
});

// Subscribe to pattern on first document subscription (handled in connections.js)
connections.onDocSubscribe = (tenantId, docId) => {
  const channel = `dms:${tenantId}:doc:${docId}`;
  redisSub.subscribe(channel);
};

connections.onDocUnsubscribe = (tenantId, docId) => {
  const channel = `dms:${tenantId}:doc:${docId}`;
  redisSub.unsubscribe(channel);
};

console.log(`collaboration ws server listening on :${PORT}/ws`);
