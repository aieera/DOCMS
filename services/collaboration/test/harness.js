/**
 * Test harness for the collaboration WS service — the first tests this
 * service has ever had (audit finding; fuller suite lands in Wave E.1).
 *
 * Pieces:
 *   - fixture HTTP server standing in for BOTH the auth service
 *     (/api/v1/auth/me) and the document service (comments CRUD, with
 *     an in-memory per-doc store) — env vars point the handler at it;
 *   - a real WebSocketServer wired to the real handleMessage +
 *     ConnectionManager, with stub Redis (single-instance: the local
 *     broadcast path is the one under test);
 *   - a ws client fixture with an inbox + waitFor(type) helper.
 *
 * Zero new dependencies: node:test + node:http + the service's own
 * `ws` package.
 */

import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { WebSocketServer, WebSocket } from 'ws';
import { onTestFinished } from 'vitest';

import { ConnectionManager } from '../src/connections.js';
import { handleMessage } from '../src/handler.js';

// ---- fixture auth + document service --------------------------------------

export function startFixtureAPI() {
  /** token → user; comments: docId → Map<commentId, comment> */
  const users = new Map();
  const comments = new Map();
  const requests = []; // every hit, for auth/assert inspection

  function docComments(docId) {
    if (!comments.has(docId)) comments.set(docId, new Map());
    return comments.get(docId);
  }

  const server = http.createServer((req, res) => {
    let bodyRaw = '';
    req.on('data', (c) => { bodyRaw += c; });
    req.on('end', () => {
      const token = (req.headers.authorization || '').replace(/^Bearer /, '');
      const user = users.get(token);
      requests.push({ method: req.method, url: req.url, token });
      const json = (code, obj) => {
        res.writeHead(code, { 'Content-Type': 'application/json' });
        res.end(obj === undefined ? '' : JSON.stringify(obj));
      };

      if (req.url === '/api/v1/auth/me') {
        if (!user) return json(401, { error: 'unauthorized' });
        return json(200, { id: user.id, tenant_id: user.tenantId, display_name: user.name });
      }
      // Everything below is the "document service": reject unknown tokens
      // (this is the ACL boundary the WS handler forwards the user's
      // token to).
      if (!user) return json(401, { error: 'unauthorized' });

      let m;
      if ((m = req.url.match(/^\/api\/v1\/documents\/([^/]+)\/comments/)) && req.method === 'POST') {
        const { body } = JSON.parse(bodyRaw);
        const c = {
          id: randomUUID(), document_id: m[1], body,
          user_id: user.id, created_at: new Date().toISOString(),
        };
        docComments(m[1]).set(c.id, c);
        return json(201, c);
      }
      if ((m = req.url.match(/^\/api\/v1\/documents\/([^/]+)\/comments/)) && req.method === 'GET') {
        return json(200, [...docComments(m[1]).values()]);
      }
      if ((m = req.url.match(/^\/api\/v1\/comments\/([^/]+)$/)) && req.method === 'PATCH') {
        for (const store of comments.values()) {
          const c = store.get(m[1]);
          if (c) {
            c.body = JSON.parse(bodyRaw).body;
            c.updated_at = new Date().toISOString();
            return json(200, c);
          }
        }
        return json(404, { error: 'not found' });
      }
      if ((m = req.url.match(/^\/api\/v1\/comments\/([^/]+)$/)) && req.method === 'DELETE') {
        for (const store of comments.values()) {
          if (store.delete(m[1])) return json(204);
        }
        return json(404, { error: 'not found' });
      }
      return json(404, { error: 'no route: ' + req.method + ' ' + req.url });
    });
  });

  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      const url = `http://127.0.0.1:${server.address().port}`;
      resolve({
        url,
        users,
        comments,
        requests,
        addUser(token, { id = randomUUID(), tenantId = 'tenant-1', name = 'Test User' } = {}) {
          users.set(token, { id, tenantId, name });
          return { id, tenantId, name };
        },
        close: () => new Promise((r) => server.close(r)),
      });
    });
  });
}

// ---- WS server under test --------------------------------------------------

const stubRedisStore = {
  sadd: async () => 1,
  srem: async () => 1,
  smembers: async () => [],
};
const stubRedisPub = { publish: async () => 0 };

export function startWSServer() {
  const connections = new ConnectionManager(stubRedisStore);
  const wss = new WebSocketServer({ port: 0, host: '127.0.0.1' });

  wss.on('connection', (ws) => {
    ws.authenticated = false;
    ws.tenantId = null;
    ws.userId = null;
    ws.subscribedDocs = new Set();
    const authTimeout = setTimeout(() => {
      if (!ws.authenticated) ws.close(4001, 'auth timeout');
    }, 5000);
    ws.on('message', async (raw) => {
      let msg;
      try { msg = JSON.parse(raw.toString()); } catch { return; }
      await handleMessage(ws, msg, connections, stubRedisPub, authTimeout);
    });
    ws.on('close', async () => {
      clearTimeout(authTimeout);
      if (ws.tenantId && ws.userId) await connections.removeConnection(ws);
    });
  });

  return new Promise((resolve) => {
    const ready = () => resolve({
      url: `ws://127.0.0.1:${wss.address().port}`,
      connections,
      close: () => new Promise((r) => {
        // wss.close() waits for clients; terminate stragglers so a
        // test that leaked a socket can't hang the suite.
        for (const c of wss.clients) c.terminate();
        wss.close(r);
      }),
    });
    // The server may already be listening by the time we attach the
    // listener (ws binds in the constructor) — resolve immediately then.
    if (wss.address()) ready();
    else wss.on('listening', ready);
  });
}

// ---- ws client fixture -------------------------------------------------------

export class TestClient {
  constructor(url) {
    this.ws = new WebSocket(url);
    this.inbox = [];
    this.closed = null;
    this.ws.on('message', (raw) => this.inbox.push(JSON.parse(raw.toString())));
    this.ws.on('close', (code, reason) => { this.closed = { code, reason: reason.toString() }; });
  }

  async open() {
    await new Promise((resolve, reject) => {
      this.ws.on('open', resolve);
      this.ws.on('error', reject);
    });
    return this;
  }

  send(obj) {
    this.ws.send(JSON.stringify(obj));
  }

  /** Wait until a message matching `pred` (or type string) arrives. */
  async waitFor(pred, timeoutMs = 3000) {
    const match = typeof pred === 'string' ? (m) => m.type === pred : pred;
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const found = this.inbox.find(match);
      if (found) return found;
      if (Date.now() > deadline) {
        throw new Error(`timed out waiting for message; inbox=${JSON.stringify(this.inbox)}`);
      }
      await new Promise((r) => setTimeout(r, 15));
    }
  }

  async waitForClose(timeoutMs = 3000) {
    const deadline = Date.now() + timeoutMs;
    while (!this.closed) {
      if (Date.now() > deadline) throw new Error('timed out waiting for close');
      await new Promise((r) => setTimeout(r, 15));
    }
    return this.closed;
  }

  async auth(token) {
    this.send({ type: 'auth', token });
    return this.waitFor('auth_ok');
  }

  async subscribe(docId) {
    this.send({ type: 'subscribe', doc_id: docId });
    return this.waitFor((m) => m.type === 'comments_snapshot' && m.doc_id === docId);
  }

  close() {
    this.ws.close();
  }
}

/**
 * Boot both fixtures with env pointed at the API stub. Cleanup is
 * registered on the currently-running test via vitest's onTestFinished,
 * so callers don't have to thread a test context through.
 */
export async function startHarness() {
  const api = await startFixtureAPI();
  process.env.AUTH_SERVICE_URL = api.url;
  process.env.DOCUMENT_SERVICE_URL = api.url;
  process.env.SEDOC_GATEWAY_SECRET = 'test-gateway-secret';
  const server = await startWSServer();
  onTestFinished(async () => {
    await server.close();
    await api.close();
  });
  return { api, server };
}
