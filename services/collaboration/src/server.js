// Collaboration WebSocket server: one port, two protocols.
//   /ws                     JSON doc-room protocol (presence + comments)
//   /yjs/{tenantId}/{docId} Yjs CRDT sync + awareness (yjs-server.js)
//
// Built as a factory so tests can run the real wiring in-process against
// fake Redis clients; index.js is only the process bootstrap.

import { WebSocketServer } from 'ws'

import { MAX_PAYLOAD_BYTES } from './config.js'
import { ConnectionManager } from './connections.js'
import { verifyGatewaySignature } from './gateway-sig.js'
import { handleMessage } from './handler.js'
import { createYjsService, SHUTDOWN_FLUSH_TIMEOUT_MS } from './yjs-server.js'
import { createRedisRelay } from './yjs-relay.js'

const HEARTBEAT_INTERVAL = 30_000

// /ws carries small JSON control frames (presence, comments); the 1 MiB
// transport cap exists for Yjs sync. Anything larger on /ws is abuse.
export const MAX_WS_JSON_FRAME_BYTES = 64 * 1024

function isWsPath(url) {
  const u = url || ''
  return u === '/ws' || u.startsWith('/ws?')
}

/**
 * ws verifyClient hook (CB-6). The /ws JSON protocol is only reachable
 * through the gateway, which stamps X-Gateway-Signature on every request
 * it forwards; an upgrade without a valid signature (a direct hit on the
 * published :8083 port) is refused with 401 before the socket exists.
 * /yjs/* authenticates each upgrade with the dms_session cookie and the
 * document permission summary instead (yjs-server.js).
 */
export function verifyUpgrade(info, cb) {
  if (isWsPath(info.req.url) && !verifyGatewaySignature(info.req.headers['x-gateway-signature'])) {
    cb(false, 401, 'invalid gateway signature')
    return
  }
  cb(true)
}

/**
 * Forward one Redis doc-room pub/sub message to local subscribers.
 * Channel format: dms:{tenantId}:doc:{docId}. A malformed payload is
 * logged and dropped; it must never throw out of the subscriber callback,
 * where it would be an uncaught exception that kills the process.
 */
export function handleRedisDocMessage(connections, channel, message) {
  const parts = String(channel).split(':')
  if (parts.length < 4 || parts[0] !== 'dms' || parts[2] !== 'doc') return
  let data
  try {
    data = JSON.parse(message)
  } catch {
    console.error(`redis: dropped non-JSON message on ${channel}`)
    return
  }
  if (!data || typeof data !== 'object' || Array.isArray(data)) return
  connections.broadcastToDoc(parts[1], parts[3], data, data._senderUserId)
}

/**
 * @param {object} opts
 * @param {number} opts.port
 * @param {string} [opts.host]
 * @param {import('ioredis').Redis} opts.redisPub   publisher
 * @param {import('ioredis').Redis} opts.redisSub   subscriber for dms:* doc rooms
 * @param {import('ioredis').Redis} opts.redisStore online-user SETs
 * @param {import('ioredis').Redis} [opts.redisYjsSub] subscriber dedicated to the
 *   Yjs cross-instance relay (CB-1); without it rooms are process-local
 * @param {object} [opts.yjsOptions] passed to createYjsService
 */
export function createCollabServer({ port, host, redisPub, redisSub, redisStore, redisYjsSub, yjsOptions = {} }) {
  const connections = new ConnectionManager(redisStore)
  const wss = new WebSocketServer({
    port,
    host,
    maxPayload: MAX_PAYLOAD_BYTES,
    verifyClient: verifyUpgrade,
  })

  // Every socket gets an 'error' listener first: ws emits 'error' for a
  // frame over maxPayload (and other protocol violations) before closing
  // with 1009/1002, and an unhandled 'error' event is an uncaught
  // exception that would take the whole process down.
  wss.on('connection', (ws) => {
    ws.on('error', (err) => {
      console.warn(`ws: socket error ${err && (err.code || err.message)}`)
    })
  })

  // Yjs path. Must be attached BEFORE the /ws handler so it sees the
  // connection first and marks it routed.
  const relay = redisYjsSub ? createRedisRelay({ pub: redisPub, sub: redisYjsSub }) : null
  const yjs = createYjsService({ relay, ...yjsOptions })
  yjs.attach(wss)

  wss.on('connection', (ws, req) => {
    if (ws.__routed === 'yjs') return
    if (!isWsPath(req.url)) {
      ws.close(1008, 'unknown path')
      return
    }

    ws.isAlive = true
    ws.authenticated = false
    ws.tenantId = null
    ws.userId = null
    ws.subscribedDocs = new Set()

    const authTimeout = setTimeout(() => {
      if (!ws.authenticated) ws.close(4001, 'auth timeout')
    }, 10_000)

    ws.on('pong', () => { ws.isAlive = true })

    ws.on('message', async (raw, isBinary) => {
      if (isBinary) {
        ws.close(1003, 'binary frames not accepted')
        return
      }
      if (raw.length > MAX_WS_JSON_FRAME_BYTES) {
        ws.close(1009, 'frame too large')
        return
      }
      let msg
      try { msg = JSON.parse(raw.toString()) } catch { return }
      if (!msg || typeof msg !== 'object') return
      try {
        await handleMessage(ws, msg, connections, redisPub, authTimeout)
      } catch (err) {
        console.error('ws: message handler failed', err)
      }
    })

    ws.on('close', async () => {
      clearTimeout(authTimeout)
      if (ws.tenantId && ws.userId) {
        try {
          await connections.removeConnection(ws)
        } catch (err) {
          console.error('ws: removeConnection failed', err)
        }
      }
    })
  })

  // Heartbeat: ping every 30s, terminate sockets that missed a pong.
  const heartbeat = setInterval(() => {
    wss.clients.forEach((ws) => {
      if (!ws.isAlive) return ws.terminate()
      ws.isAlive = false
      ws.ping()
    })
  }, HEARTBEAT_INTERVAL)
  heartbeat.unref?.()

  redisSub.on('message', (channel, message) => {
    handleRedisDocMessage(connections, channel, message)
  })
  connections.onDocSubscribe = (tenantId, docId) => {
    Promise.resolve(redisSub.subscribe(`dms:${tenantId}:doc:${docId}`))
      .catch((err) => console.error('redis: subscribe failed', err))
  }
  connections.onDocUnsubscribe = (tenantId, docId) => {
    Promise.resolve(redisSub.unsubscribe(`dms:${tenantId}:doc:${docId}`))
      .catch((err) => console.error('redis: unsubscribe failed', err))
  }

  const listening = new Promise((resolve, reject) => {
    if (wss.address()) return resolve()
    wss.once('listening', resolve)
    wss.once('error', reject)
  })

  let shuttingDown = null

  const server = {
    wss,
    yjs,
    connections,
    listening,
    get port() {
      const a = wss.address()
      return a && typeof a === 'object' ? a.port : undefined
    },
    /**
     * Graceful stop (SIGTERM, CB-5): stop accepting upgrades, flush every
     * dirty Yjs room (bounded at SHUTDOWN_FLUSH_TIMEOUT_MS), then close the
     * remaining sockets with 1012 (service restart) so clients reconnect
     * to another instance. Idempotent.
     */
    shutdown({ timeoutMs = SHUTDOWN_FLUSH_TIMEOUT_MS } = {}) {
      if (shuttingDown) return shuttingDown
      shuttingDown = (async () => {
        clearInterval(heartbeat)
        const closed = new Promise((resolve) => wss.close(() => resolve()))
        const result = await yjs.flushAll({ timeoutMs })
        for (const c of wss.clients) {
          try { c.close(1012, 'server restarting') } catch { /* already closing */ }
        }
        // Give close frames a moment, then drop whatever is left.
        await new Promise((resolve) => setTimeout(resolve, 200))
        for (const c of wss.clients) c.terminate()
        await closed
        return result
      })()
      return shuttingDown
    },
    close() {
      clearInterval(heartbeat)
      return new Promise((resolve) => {
        for (const c of wss.clients) c.terminate()
        wss.close(() => resolve())
      })
    },
  }
  return server
}
