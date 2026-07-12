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
 *   - installFetchMock(): stubs the auth /auth/me + policy /permissions/check
 *     HTTP calls that authorize() makes, so tenant/ACL outcomes are scripted
 *     without standing up the auth or policy services.
 *
 * The Postgres snapshot layer (yjs-persistence.js) is module-mocked in the
 * test file itself (vi.mock), not here — the store needs to be inspectable
 * per test.
 */

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

// ---- server under test -----------------------------------------------------

export function startYjsServer() {
  const wss = new WebSocketServer({ port: 0, host: '127.0.0.1' })
  attachYjs(wss)
  return new Promise((resolve) => {
    const ready = () => {
      const base = `ws://127.0.0.1:${wss.address().port}`
      const handle = {
        base,
        wss,
        // ws://…/yjs/{tenant}/{doc}
        yjsURL: (tenantId, docId) => `${base}/yjs/${tenantId}/${docId}`,
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
 *   userId     — user_id returned by /auth/me
 *   viewAllowed / editAllowed — policy answers for the 'view' / 'edit' action.
 *     Either a boolean, or a `(cookie) => boolean` to answer per-client (the
 *     Cookie header authorize forwards), e.g. one editor + one read-only.
 *   meThrows / policyThrows   — simulate a transport error (→ 4503)
 * Returns the vi.fn so a test can assert on calls. Auto-unstubbed on finish.
 */
export function installFetchMock({
  meStatus = 200,
  tenantId = 'tenant-1',
  userId = 'user-1',
  viewAllowed = true,
  editAllowed = true,
  meThrows = false,
  policyThrows = false,
} = {}) {
  const resolve = (val, cookie) => (typeof val === 'function' ? val(cookie) : val)
  const fn = vi.fn(async (url, opts = {}) => {
    const u = String(url)
    const cookie = opts.headers?.Cookie || ''
    if (u.includes('/api/v1/auth/me')) {
      if (meThrows) throw new Error('auth service unreachable')
      if (meStatus !== 200) return jsonResponse(meStatus, { error: 'nope' })
      return jsonResponse(200, { tenant_id: tenantId, user_id: userId })
    }
    if (u.includes('/api/v1/permissions/check')) {
      if (policyThrows) throw new Error('policy service unreachable')
      const action = JSON.parse(opts.body || '{}').action
      const allowed = action === 'edit'
        ? resolve(editAllowed, cookie) === true
        : resolve(viewAllowed, cookie) === true
      return jsonResponse(200, { allowed })
    }
    return jsonResponse(404, { error: 'no route' })
  })
  vi.stubGlobal('fetch', fn)
  onTestFinished(() => vi.unstubAllGlobals())
  return fn
}

// ---- Yjs client ------------------------------------------------------------

const DEFAULT_COOKIE = 'dms_session=test-session'

/**
 * Minimal Yjs sync client over a raw WebSocket — the client half of the
 * handshake src/yjs-server.js implements. `cookie: null` omits the Cookie
 * header entirely (for the no-auth 4401 case).
 */
export class YjsTestClient {
  constructor(url, { cookie = DEFAULT_COOKIE } = {}) {
    this.doc = new Y.Doc()
    this.awareness = new awarenessProtocol.Awareness(this.doc)
    const opts = cookie == null ? {} : { headers: { Cookie: cookie } }
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
