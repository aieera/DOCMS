import { WebSocketServer } from 'ws';
import { createClient } from './redis.js';
import { ConnectionManager } from './connections.js';
import { handleMessage } from './handler.js';

const PORT = parseInt(process.env.WS_PORT || '8083', 10);
const HEARTBEAT_INTERVAL = 30_000;
const PONG_TIMEOUT = 10_000;

const redisSub = createClient();
const redisPub = createClient();
const redisStore = createClient();
const connections = new ConnectionManager(redisStore);

const wss = new WebSocketServer({ port: PORT, path: '/ws' });

wss.on('connection', (ws, req) => {
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
