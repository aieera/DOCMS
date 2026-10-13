// M-30: extension background service worker.
//   - OAuth PKCE state lived only in MV3 service-worker memory; Chrome
//     stops an idle worker after ~30 s, so by the time the user finished
//     on the IdP the state was gone and login failed "state mismatch".
//     It now lives in chrome.storage.session.
//   - The upload chain read `upload_url` (the backend returns
//     `presigned_put_url`), omitted the required folder_id, and passed
//     content_blob_id to CreateDocument (dropped by the gateway) instead
//     of creating a version. It now: workspace → root folder → initiate
//     (folder_id) → PUT presigned_put_url → complete → CreateDocument
//     (folder_id) → CreateVersion(content_blob_id).
//
// Runs with vitest globals (see package.json "test").

function makeChrome() {
  const areas = { local: new Map(), session: new Map() }
  // chrome.storage is IPC: every call yields to the event loop, which is
  // what lets two unguarded read-modify-write cycles interleave.
  const tick = () => new Promise((r) => setTimeout(r, 0))
  const area = (m) => ({
    async get(key) {
      await tick()
      if (typeof key === 'string') return m.has(key) ? { [key]: structuredClone(m.get(key)) } : {}
      return Object.fromEntries([...m].map(([k, v]) => [k, structuredClone(v)]))
    },
    async set(obj) { await tick(); for (const [k, v] of Object.entries(obj)) m.set(k, structuredClone(v)) },
    async remove(key) { m.delete(key) },
  })
  const listeners = []
  return {
    _areas: areas,
    _listeners: listeners,
    storage: { local: area(areas.local), session: area(areas.session) },
    runtime: {
      onMessage: { addListener: (fn) => listeners.push(fn) },
      getURL: (p) => `chrome-extension://abc/${p}`,
    },
    notifications: { create: vi.fn(async () => 'n') },
    tabs: { query: vi.fn(async () => []) },
  }
}

async function send(chromeObj, msg) {
  for (const fn of chromeObj._listeners) {
    let resolve
    const p = new Promise((r) => { resolve = r })
    const keepOpen = fn(msg, {}, resolve)
    if (keepOpen === true) return p
  }
  throw new Error('no listener handled ' + msg.type)
}

async function loadWorker() {
  vi.resetModules()
  return import('../background.js')
}

function json(body, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

let chromeObj
beforeEach(() => {
  chromeObj = makeChrome()
  globalThis.chrome = chromeObj
  globalThis.fetch = vi.fn()
})

describe('OAuth state survives a service-worker restart (M-30)', () => {
  it('keeps the PKCE state in chrome.storage.session', async () => {
    await loadWorker()
    const res = await send(chromeObj, { type: 'login' })
    expect(res.ok).toBe(true)
    const state = new URL(res.url).searchParams.get('state')
    expect(state).toBeTruthy()
    const saved = chromeObj._areas.session.get('oauth_states')
    expect(saved?.[state]?.codeVerifier).toBeTruthy()
    // Never in persistent local storage.
    expect(JSON.stringify([...chromeObj._areas.local])).not.toContain(state)
  })

  it('completes the callback after the worker was torn down and restarted', async () => {
    await loadWorker()
    const { url } = await send(chromeObj, { type: 'login' })
    const state = new URL(url).searchParams.get('state')

    // Worker killed while the user is on the IdP: fresh module instance.
    chromeObj._listeners.length = 0
    await loadWorker()

    fetch.mockResolvedValueOnce(json({ access_token: 'at', refresh_token: 'rt', expires_in: 3600 }))
    const res = await send(chromeObj, { type: 'oauth-callback', state, code: 'c0de' })
    expect(res).toEqual({ ok: true })
    const body = fetch.mock.calls[0][1].body
    expect(body.get('code_verifier')).toBeTruthy()
    // One-time use.
    expect(chromeObj._areas.session.get('oauth_states')?.[state]).toBeUndefined()
  })

  // Review (round 1): saveOAuthState / takeOAuthState were unguarded
  // read-modify-write cycles on one storage key; two logins in flight
  // (popup double-click, two windows) lost one state.
  it('concurrent logins keep every state', async () => {
    await loadWorker()
    const [a, b, c] = await Promise.all([
      send(chromeObj, { type: 'login' }),
      send(chromeObj, { type: 'login' }),
      send(chromeObj, { type: 'login' }),
    ])
    const saved = chromeObj._areas.session.get('oauth_states')
    for (const r of [a, b, c]) {
      expect(saved[new URL(r.url).searchParams.get('state')]).toBeTruthy()
    }
  })

  it('a state can be taken only once, even by concurrent callbacks', async () => {
    await loadWorker()
    const { url } = await send(chromeObj, { type: 'login' })
    const state = new URL(url).searchParams.get('state')
    fetch.mockResolvedValue(json({ access_token: 'at', expires_in: 3600 }))
    const results = await Promise.all([
      send(chromeObj, { type: 'oauth-callback', state, code: 'c1' }),
      send(chromeObj, { type: 'oauth-callback', state, code: 'c2' }),
    ])
    expect(results.filter((r) => r.ok)).toHaveLength(1)
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('rejects an unknown state', async () => {
    await loadWorker()
    const res = await send(chromeObj, { type: 'oauth-callback', state: 'nope', code: 'x' })
    expect(res).toEqual({ ok: false, error: 'state mismatch' })
    expect(fetch).not.toHaveBeenCalled()
  })
})

describe('upload chain (M-30)', () => {
  it('initiates with a folder, PUTs presigned_put_url, completes, creates document + version', async () => {
    chromeObj._areas.local.set('vaultdms_session', { access_token: 'at', expires_at: Date.now() + 3_600_000 })
    await loadWorker()
    const calls = []
    fetch.mockImplementation(async (url, init = {}) => {
      const u = String(url)
      const method = init.method || 'GET'
      calls.push({ method, url: u, body: init.body })
      if (method === 'GET' && u.endsWith('/api/v1/workspaces?limit=1')) return json({ workspaces: [{ id: 'ws-1' }] })
      if (method === 'GET' && u.includes('/api/v1/workspaces/ws-1/folders')) {
        return json({ folders: [{ id: 'f-quotes', name: 'Quotes', created_at: '2026-01-01' }, { id: 'f-root', name: 'Root' }] })
      }
      if (u.endsWith('/api/v1/storage/uploads/initiate')) return json({ upload_id: 'up-1', presigned_put_url: 'https://s3.example/put?sig=1' })
      if (method === 'PUT' && u === 'https://s3.example/put?sig=1') return new Response(null, { status: 200 })
      if (u.endsWith('/api/v1/storage/uploads/up-1/complete')) return json({ content_blob_id: 'blob-1' })
      if (method === 'POST' && u.endsWith('/api/v1/documents')) return json({ id: 'doc-1' })
      if (method === 'POST' && u.endsWith('/api/v1/documents/doc-1/versions')) return json({ id: 'ver-1' })
      return json({ error: 'unexpected ' + method + ' ' + u }, 500)
    })

    const res = await send(chromeObj, {
      type: 'upload',
      file: { name: 'a.pdf', mimeType: 'application/pdf', sizeBytes: 3, sha256: 'ab', bytesBase64: btoa('abc') },
    })
    expect(res).toEqual({ ok: true, documentId: 'doc-1' })

    const initiate = calls.find((c) => c.url.endsWith('/uploads/initiate'))
    expect(JSON.parse(initiate.body)).toMatchObject({ workspace_id: 'ws-1', folder_id: 'f-root', filename: 'a.pdf' })
    expect(calls.some((c) => c.method === 'PUT' && c.url === 'https://s3.example/put?sig=1')).toBe(true)
    const doc = calls.find((c) => c.method === 'POST' && c.url.endsWith('/api/v1/documents'))
    expect(JSON.parse(doc.body)).toEqual({ workspace_id: 'ws-1', folder_id: 'f-root', title: 'a.pdf' })
    const ver = calls.find((c) => c.url.endsWith('/documents/doc-1/versions'))
    expect(JSON.parse(ver.body)).toMatchObject({ content_blob_id: 'blob-1' })
    // Bytes stored before the document row exists.
    const order = (pred) => calls.findIndex(pred)
    expect(order((c) => c.url.endsWith('/complete'))).toBeLessThan(order((c) => c.url.endsWith('/api/v1/documents')))
  })

  it('fails cleanly when complete returns no blob id', async () => {
    chromeObj._areas.local.set('vaultdms_session', { access_token: 'at', expires_at: Date.now() + 3_600_000 })
    await loadWorker()
    fetch.mockImplementation(async (url, init = {}) => {
      const u = String(url)
      if (u.endsWith('/api/v1/workspaces?limit=1')) return json({ workspaces: [{ id: 'ws-1' }] })
      if (u.includes('/folders')) return json({ folders: [{ id: 'f-root', name: 'Root' }] })
      if (u.endsWith('/initiate')) return json({ upload_id: 'up-1', presigned_put_url: 'https://s3.example/put' })
      if ((init.method || 'GET') === 'PUT') return new Response(null, { status: 200 })
      if (u.endsWith('/complete')) return json({})
      return json({ error: 'should not be called' }, 500)
    })
    const res = await send(chromeObj, {
      type: 'upload',
      file: { name: 'a.pdf', mimeType: 'application/pdf', sizeBytes: 3, sha256: 'ab', bytesBase64: btoa('abc') },
    })
    expect(res.ok).toBe(false)
    expect(fetch.mock.calls.some(([u]) => String(u).endsWith('/api/v1/documents'))).toBe(false)
  })
})
