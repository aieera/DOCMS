/**
 * Test harness for the Yjs CRDT path (src/yjs-server.js, ADR 0096).
 *
 * Unlike the /ws JSON protocol (see harness.js), /yjs speaks the binary
 * y-protocols sync + awareness framing. This harness gives tests:
 *   - startYjsServer(): a real ws.WebSocketServer with attachYjs() wired,
 *     on a random port, cleaned up via vitest's onTestFinished;
 *   - YjsTestClient: a minimal client-side sync provider around a Y.Doc
 *     (the same handshake y-websocket performs) so two clients can be
 *     asserted to converge, with cookie/header control for the auth tests;
 *   - installFetchMock(): stubs the auth /auth/me + document permission
 *     summary HTTP calls that authorize() makes, so tenant/ACL outcomes are
 *     scripted without standing up the auth or document services.
 *
 * The Postgres snapshot layer (yjs-persistence.js) is module-mocked in the
 * test file itself (vi.mock), not here — the store needs to be inspectable
 * per test.
 */

import { createHash } from 'node:crypto'
import { WebSocketServer, WebSocket } from 'ws'
import { onTestFinished, vi } from 'vitest'
import * as Y from 'yjs'
import * as awarenessProtocol from 'y-protocols/awareness'
import * as syncProtocol from 'y-protocols/sync'
import * as encoding from 'lib0/encoding'
import * as decoding from 'lib0/decoding'

import { attachYjs } from '../src/yjs-server.js'

const messageSync = 0
const messageAwareness = 1

// ---- ids -------------------------------------------------------------------

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/** Deterministic UUID for a readable test name ('doc-x' → stable uuid). */
export function uid(name) {
  const h = createHash('sha1').update(String(name)).digest('hex')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20, 32)}`
}

/** A UUID stays as is; any other name maps to uid(name). */
export function toId(x) {
  return UUID_RE.test(String(x)) ? String(x) : uid(x)
}

/** The tenant every /auth/me answer uses unless a test overrides it. */
export const TENANT_ID = uid('tenant-1')

/** Room registry key for (tenant, doc) names — canonical lower-case UUIDs. */
export function roomKey(tenant, doc) {
  return `${toId(tenant).toLowerCase()}:${toId(doc).toLowerCase()}`
}

// ---- server under test -----------------------------------------------------

export function startYjsServer({ service } = {}) {
  const wss = new WebSocketServer({ port: 0, host: '127.0.0.1' })
  if (service) service.attach(wss)
  else attachYjs(wss)
  return new Promise((resolve) => {
    const ready = () => {
      const base = `ws://127.0.0.1:${wss.address().port}`
      const handle = {
        base,
        wss,
        // ws://…/yjs/{tenant}/{doc}; readable names map to stable UUIDs
        yjsURL: (tenantId, docId) => `${base}/yjs/${toId(tenantId)}/${toId(docId)}`,
        close: () =>
          new Promise((r) => {
            for (const c of wss.clients) c.terminate()
            wss.close(r)
          }),
      }
      onTestFinished(() => handle.close())
      resolve(handle)
    }
    if (wss.address()) ready()
    else wss.on('listening', ready)
  })
}

// ---- fetch mock for authorize()/checkDocPermission() -----------------------

function jsonResponse(status, body) {
  return new Response(body === undefined ? '' : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/**
 * Stub global fetch for the two endpoints authorize() hits. Scenario:
 *   meStatus   — status of /api/v1/auth/me (200 to authenticate)
 *   tenantId   — tenant_id returned by /auth/me (compared to the URL room)
 *   userId     — id returned by /auth/me
 *   viewAllowed / editAllowed — the document service's permission summary
 *     (GET /api/v1/documents/{id} → permissions.can_view / can_edit).
 *     Either a boolean, or a `(cookie) => boolean` to answer per-client (the
 *     Cookie header authorize forwards), e.g. one editor + one read-only.
 *     Re-evaluated on every call, so a test can flip it mid-session.
 *   docStatus  — non-200 status for the document GET (e.g. 404, 401, 500);
 *     a `(cookie) => number|undefined` is re-evaluated per call.
 *   meThrows / docThrows — simulate a transport error (→ 4503)
 * Returns the vi.fn so a test can assert on calls. Auto-unstubbed on finish.
 */
export function installFetchMock({
  meStatus = 200,
  tenantId = TENANT_ID,
  userId = 'user-1',
  viewAllowed = true,
  editAllowed = true,
  docStatus = undefined,
  meThrows = false,
  docThrows = false,
} = {}) {
  const resolve = (val, cookie) => (typeof val === 'function' ? val(cookie) : val)
  const fn = vi.fn(async (url, opts = {}) => {
    const u = String(url)
    const cookie = opts.headers?.Cookie || ''
    if (u.includes('/api/v1/auth/me')) {
      if (meThrows) throw new Error('auth service unreachable')
      if (meStatus !== 200) return jsonResponse(meStatus, { error: 'nope' })
      return jsonResponse(200, { id: userId, tenant_id: toId(tenantId) })
    }
    const m = /\/api\/v1\/documents\/([^/?]+)$/.exec(u)
    if (m && (opts.method || 'GET') === 'GET') {
      if (resolve(docThrows, cookie) === true) throw new Error('document service unreachable')
      const status = resolve(docStatus, cookie)
      if (status && status !== 200) return jsonResponse(status, { error: 'nope' })
      const view = resolve(viewAllowed, cookie) === true
      // Like GetDocument: no view → 403 before any summary is returned.
      if (!view) return jsonResponse(403, { error: 'forbidden' })
      return jsonResponse(200, {
        id: decodeURIComponent(m[1]),
        permissions: {
          can_view: true,
          can_edit: resolve(editAllowed, cookie) === true,
          can_delete: false,
          can_share: false,
          can_admin: false,
        },
      })
    }
    return jsonResponse(404, { error: 'no route' })
  })
  vi.stubGlobal('fetch', fn)
  onTestFinished(() => vi.unstubAllGlobals())
  return fn
}

// ---- Yjs client ------------------------------------------------------------

const DEFAULT_COOKIE = 'dms_session=test-session'
/** The allow-listed browser origin (vitest.config.js SEDOC_ALLOWED_ORIGINS). */
export const ALLOWED_ORIGIN = 'https://app.sedoc.test'

/**
 * Minimal Yjs sync client over a raw WebSocket — the client half of the
 * handshake src/yjs-server.js implements. `cookie: null` omits the Cookie
 * header entirely (for the no-auth 4401 case).
 */
export class YjsTestClient {
  constructor(url, { cookie = DEFAULT_COOKIE, origin = ALLOWED_ORIGIN } = {}) {
    this.doc = new Y.Doc()
    this.awareness = new awarenessProtocol.Awareness(this.doc)
    // Like a browser: the session cookie arrives with the page's Origin.
    const headers = {}
    if (cookie != null) headers.Cookie = cookie
    if (origin != null) headers.Origin = origin
    const opts = { headers }
    this.ws = new WebSocket(url, opts)
    this.closed = null
    this.ws.on('close', (code, reason) => {
      this.closed = { code, reason: reason?.toString() ?? '' }
    })
    // Swallow errors (a 4xxx close right after upgrade surfaces as an
    // error on some platforms) so they don't become unhandled rejections.
    this.ws.on('error', () => {})
    this.ws.on('message', (raw) => this._onMessage(raw))

    // Relay local doc mutations to the server (skip echoes of applied
    // remote updates, tagged origin 'remote').
    this.doc.on('update', (update, origin) => {
      if (origin === 'remote') return
      const enc = encoding.createEncoder()
      encoding.writeVarUint(enc, messageSync)
      syncProtocol.writeUpdate(enc, update)
      this._send(encoding.toUint8Array(enc))
    })
  }

  _send(u8) {
    if (this.ws.readyState === 1) this.ws.send(u8)
  }

  _onMessage(raw) {
    const dec = decoding.createDecoder(new Uint8Array(raw))
    const kind = decoding.readVarUint(dec)
    if (kind === messageSync) {
      const enc = encoding.createEncoder()
      encoding.writeVarUint(enc, messageSync)
      syncProtocol.readSyncMessage(dec, enc, this.doc, 'remote')
      if (encoding.length(enc) > 1) this._send(encoding.toUint8Array(enc))
    } else if (kind === messageAwareness) {
      awarenessProtocol.applyAwarenessUpdate(
        this.awareness,
        decoding.readVarUint8Array(dec),
        'remote',
      )
    }
  }

  /** Resolve once the socket is open, then send the initial sync step 1. */
  async open() {
    await new Promise((resolve, reject) => {
      this.ws.once('open', resolve)
      this.ws.once('error', reject)
    })
    const enc = encoding.createEncoder()
    encoding.writeVarUint(enc, messageSync)
    syncProtocol.writeSyncStep1(enc, this.doc)
    this._send(encoding.toUint8Array(enc))
    return this
  }

  /** The shared text type used by the convergence tests. */
  text() {
    return this.doc.getText('content')
  }

  /** Poll until `pred(this)` is truthy (default: text equals `expected`). */
  async waitUntil(pred, timeoutMs = 3000) {
    const deadline = Date.now() + timeoutMs
    for (;;) {
      if (pred(this)) return true
      if (Date.now() > deadline) {
        throw new Error(`timed out; text=${JSON.stringify(this.text().toString())}`)
      }
      await new Promise((r) => setTimeout(r, 15))
    }
  }

  async waitForText(expected, timeoutMs = 3000) {
    return this.waitUntil((c) => c.text().toString() === expected, timeoutMs)
  }

  async waitForClose(timeoutMs = 3000) {
    const deadline = Date.now() + timeoutMs
    while (!this.closed) {
      if (Date.now() > deadline) throw new Error('timed out waiting for close')
      await new Promise((r) => setTimeout(r, 15))
    }
    return this.closed
  }

  close() {
    try {
      this.ws.close()
    } catch {
      /* already closing */
    }
  }
}
