import { randomUUID } from 'crypto';
import { createServer } from 'http';
import { WebSocketServer } from 'ws';
import { createClient } from './redis.js';
import { ConnectionManager } from './connections.js';
import { handleMessage } from './handler.js';
import { verifyUpgrade } from './auth.js';
import { CLOSE_UNAUTHENTICATED } from './schemas.js';
import { startAnnotationBridge } from './nats-bridge.js';
import { attachYjs } from './yjs-server.js';

const PORT = parseInt(process.env.WS_PORT || '8083', 10);
const HEARTBEAT_INTERVAL = 30_000;
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

// noServer mode: we own the HTTP upgrade handshake so we can verify
// cookie + CSRF BEFORE the WebSocket handshake completes. This matches
// the brief's "auth middleware verifies dms_session cookie + CSRF
// token in the handshake" requirement — a rejected upgrade returns
// 401 at HTTP level rather than an authenticated-but-useless socket.
const wss = new WebSocketServer({ noServer: true });

// §17.4 / E7 — Yjs path. Keeps its own auth scheme (token in URL)
// since that protocol predates cookie auth; retained for back-compat.
attachYjs(wss);

const httpServer = createServer((_req, res) => {
  res.writeHead(426); // Upgrade Required
  res.end('this endpoint serves WebSocket connections only');
});

httpServer.on('upgrade', async (req, socket, head) => {
  const url = req.url || '';
  // Yjs keeps its own auth; dispatch to it without cookie verification.
  if (url.startsWith('/yjs/')) {
    wss.handleUpgrade(req, socket, head, (ws) => wss.emit('connection', ws, req));
    return;
  }
  if (url !== '/ws' && !url.startsWith('/ws?')) {
    socket.write('HTTP/1.1 404 Not Found\r\n\r\n');
    socket.destroy();
    return;
  }

  const user = await verifyUpgrade(req);
  if (!user) {
    // Pre-handshake HTTP 401. Client never sees a WS socket, so this
    // is more informative than a mid-protocol close frame.
    socket.write('HTTP/1.1 401 Unauthorized\r\n\r\n');
    socket.destroy();
    return;
  }
  wss.handleUpgrade(req, socket, head, (ws) => {
    ws.isAlive = true;
    ws.tenantId = user.tenantId;
    ws.userId = user.userId;
    ws.displayName = user.displayName;
    ws.cookie = user.cookie;
    ws.subscribedDocs = new Set();
    wss.emit('connection', ws, req);
  });
});

wss.on('connection', (ws, req) => {
  // Yjs path was claimed by attachYjs before we get here.
  if (ws.__routed === 'yjs') return;

  connections.addConnection(ws).catch(() => {});

  ws.on('pong', () => { ws.isAlive = true; });
  ws.on('message', async (raw) => {
    try {
      await handleMessage(ws, raw, connections, redisPub);
    } catch (err) {
      console.error('handler error:', err);
      ws.close(CLOSE_UNAUTHENTICATED, 'handler error');
    }
  });
  ws.on('close', async () => {
    if (ws.tenantId && ws.userId) {
      await connections.removeConnection(ws);
    }
  });
});

// Heartbeat: ping every 30s, terminate if the previous pong never arrived.
setInterval(() => {
  wss.clients.forEach((ws) => {
    if (!ws.isAlive) return ws.terminate();
    ws.isAlive = false;
    ws.ping();
  });
}, HEARTBEAT_INTERVAL);

// Cross-instance forward: messages a peer pod published land here via
// the subscriber; broadcast them to local sockets only.
redisSub.on('message', (channel, message) => {
  const parts = channel.split(':');
  if (parts.length < 4) return;
  const tenantId = parts[1];
  const docId = parts[3];
  let data;
  try { data = JSON.parse(message); } catch { return; }
  connections.broadcastToDoc(tenantId, docId, data, data._senderUserId);
});

connections.onDocSubscribe = (tenantId, docId) => {
  redisSub.subscribe(`dms:${tenantId}:doc:${docId}`);
};
connections.onDocUnsubscribe = (tenantId, docId) => {
  redisSub.unsubscribe(`dms:${tenantId}:doc:${docId}`);
};

httpServer.listen(PORT, () => {
  console.log(`collaboration ws server listening on :${PORT}/ws (cookie+CSRF upgrade)`);
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
