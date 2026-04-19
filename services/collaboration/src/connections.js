/**
 * ConnectionManager tracks authenticated WebSocket connections per tenant/user
 * and manages document channel subscriptions. Uses Redis SETs for online user
 * tracking (cross-instance).
 */
export class ConnectionManager {
  constructor(redis) {
    this.redis = redis;
    // Map<tenantId, Map<userId, Set<ws>>>
    this.tenants = new Map();
    // Map<tenantId:docId, Set<ws>>
    this.docSubscribers = new Map();
    this.onDocSubscribe = null;
    this.onDocUnsubscribe = null;
  }

  async addConnection(ws) {
    const { tenantId, userId } = ws;
    if (!this.tenants.has(tenantId)) this.tenants.set(tenantId, new Map());
    const users = this.tenants.get(tenantId);
    if (!users.has(userId)) users.set(userId, new Set());
    users.get(userId).add(ws);
    await this.redis.sadd(`online:${tenantId}`, userId);
  }

  async removeConnection(ws) {
    const { tenantId, userId } = ws;
    const users = this.tenants.get(tenantId);
    if (users) {
      const sockets = users.get(userId);
      if (sockets) {
        sockets.delete(ws);
        if (sockets.size === 0) {
          users.delete(userId);
          await this.redis.srem(`online:${tenantId}`, userId);
        }
      }
      if (users.size === 0) this.tenants.delete(tenantId);
    }
    for (const docId of ws.subscribedDocs) {
      this._unsubDoc(ws, tenantId, docId);
    }
  }

  subscribeToDoc(ws, docId) {
    const key = `${ws.tenantId}:${docId}`;
    if (!this.docSubscribers.has(key)) {
      this.docSubscribers.set(key, new Set());
      this.onDocSubscribe?.(ws.tenantId, docId);
    }
    this.docSubscribers.get(key).add(ws);
    ws.subscribedDocs.add(docId);
  }

  unsubscribeFromDoc(ws, docId) {
    this._unsubDoc(ws, ws.tenantId, docId);
  }

  _unsubDoc(ws, tenantId, docId) {
    const key = `${tenantId}:${docId}`;
    const subs = this.docSubscribers.get(key);
    if (subs) {
      subs.delete(ws);
      if (subs.size === 0) {
        this.docSubscribers.delete(key);
        this.onDocUnsubscribe?.(tenantId, docId);
      }
    }
    ws.subscribedDocs.delete(docId);
  }

  broadcastToDoc(tenantId, docId, data, excludeUserId) {
    const key = `${tenantId}:${docId}`;
    const subs = this.docSubscribers.get(key);
    if (!subs) return;
    const msg = JSON.stringify(data);
    for (const ws of subs) {
      if (ws.userId === excludeUserId) continue;
      if (ws.readyState === 1) ws.send(msg);
    }
  }

  sendToUser(tenantId, userId, data) {
    const users = this.tenants.get(tenantId);
    if (!users) return;
    const sockets = users.get(userId);
    if (!sockets) return;
    const msg = JSON.stringify(data);
    for (const ws of sockets) {
      if (ws.readyState === 1) ws.send(msg);
    }
  }

  async getOnlineUsers(tenantId) {
    return this.redis.smembers(`online:${tenantId}`);
  }

  getDocPresence(tenantId, docId) {
    const key = `${tenantId}:${docId}`;
    const subs = this.docSubscribers.get(key);
    if (!subs) return [];
    const users = new Set();
    for (const ws of subs) users.add(ws.userId);
    return [...users];
  }
}
