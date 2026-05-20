// VaultDMS extension — background service worker.
// ADR 0097 §17.7. Phase 1: OAuth state machine + message bus.
//
// Responsibilities:
//   - Hold the access_token / refresh_token in chrome.storage.local.
//   - Run the PKCE OAuth dance on first install (and on token expiry).
//   - Route messages between popup/content scripts and the DMS API.
//   - Drive uploads (storage proxy chain) and clippings.

const STORAGE_KEY = 'vaultdms_session'

// dms_base is selectable via the options page; default to the dev URL.
// In a release build, the manifest's host_permissions limits which
// origins the extension can hit, so even a misconfigured base falls
// back to permission-denied rather than free-form egress.
async function dmsBase() {
  const { dms_base } = await chrome.storage.local.get('dms_base')
  return dms_base || 'http://localhost:3000'
}

// ---- PKCE helpers ---------------------------------------------------

function randomString(bytes = 32) {
  const arr = new Uint8Array(bytes)
  crypto.getRandomValues(arr)
  return Array.from(arr, (b) => b.toString(16).padStart(2, '0')).join('')
}

async function sha256Base64Url(input) {
  const data = new TextEncoder().encode(input)
  const hash = await crypto.subtle.digest('SHA-256', data)
  return base64UrlEncode(new Uint8Array(hash))
}

function base64UrlEncode(bytes) {
  let s = ''
  for (const b of bytes) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// ---- OAuth state ----------------------------------------------------

const OAUTH_STATE = new Map() // state → { codeVerifier, createdAt }

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  // sendResponse must be called or returned-true synchronously to keep
  // the channel open for async work. We wrap all handlers in an IIFE.
  ;(async () => {
    try {
      switch (msg?.type) {
        case 'login':
          sendResponse({ ok: true, url: await beginOAuth() })
          break
        case 'session':
          sendResponse({ ok: true, session: await loadSession() })
          break
        case 'logout':
          await chrome.storage.local.remove(STORAGE_KEY)
          sendResponse({ ok: true })
          break
        case 'api':
          sendResponse(await apiCall(msg.path, msg.init || {}))
          break
        case 'upload':
          sendResponse(await uploadFile(msg.file))
          break
        case 'clip':
          sendResponse(await clipActiveTab())
          break
        default:
          sendResponse({ ok: false, error: 'unknown message' })
      }
    } catch (err) {
      sendResponse({ ok: false, error: String(err) })
    }
  })()
  return true
})

async function beginOAuth() {
  const codeVerifier = randomString(48)
  const codeChallenge = await sha256Base64Url(codeVerifier)
  const state = randomString(16)
  OAUTH_STATE.set(state, { codeVerifier, createdAt: Date.now() })
  // Garbage collect entries older than 10 min so map doesn't grow.
  for (const [k, v] of OAUTH_STATE) {
    if (Date.now() - v.createdAt > 10 * 60_000) OAUTH_STATE.delete(k)
  }

  const base = await dmsBase()
  const redirectUri = chrome.runtime.getURL('callback.html')
  const url = new URL(`${base}/api/v1/oauth/authorize`)
  url.searchParams.set('client_id', 'vaultdms-ext')
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('code_challenge', codeChallenge)
  url.searchParams.set('code_challenge_method', 'S256')
  url.searchParams.set('redirect_uri', redirectUri)
  url.searchParams.set('state', state)
  return url.toString()
}

// callback.html posts here once the auth tab redirects with ?code=...
chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg?.type !== 'oauth-callback') return false
  ;(async () => {
    const saved = OAUTH_STATE.get(msg.state)
    if (!saved) {
      sendResponse({ ok: false, error: 'state mismatch' })
      return
    }
    OAUTH_STATE.delete(msg.state)
    const base = await dmsBase()
    const tokenRes = await fetch(`${base}/api/v1/oauth/token`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({
        grant_type: 'authorization_code',
        client_id: 'vaultdms-ext',
        code: msg.code,
        code_verifier: saved.codeVerifier,
        redirect_uri: chrome.runtime.getURL('callback.html'),
      }),
    })
    if (!tokenRes.ok) {
      sendResponse({ ok: false, error: `token exchange failed: ${tokenRes.status}` })
      return
    }
    const tokens = await tokenRes.json()
    await chrome.storage.local.set({
      [STORAGE_KEY]: {
        ...tokens,
        // Anchor expiry on wall-clock now so subsequent refresh logic
        // can compute time-to-expire without parsing the JWT.
        expires_at: Date.now() + (tokens.expires_in || 3600) * 1000,
      },
    })
    sendResponse({ ok: true })
  })()
  return true
})

async function loadSession() {
  const got = await chrome.storage.local.get(STORAGE_KEY)
  return got[STORAGE_KEY] || null
}

async function refreshIfNeeded(session) {
  if (!session) return null
  if (Date.now() < session.expires_at - 60_000) return session
  if (!session.refresh_token) return null
  const base = await dmsBase()
  const res = await fetch(`${base}/api/v1/oauth/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({
      grant_type: 'refresh_token',
      client_id: 'vaultdms-ext',
      refresh_token: session.refresh_token,
    }),
  })
  if (!res.ok) return null
  const tokens = await res.json()
  const next = {
    ...tokens,
    expires_at: Date.now() + (tokens.expires_in || 3600) * 1000,
  }
  await chrome.storage.local.set({ [STORAGE_KEY]: next })
  return next
}

// ---- DMS API call helper -------------------------------------------

async function apiCall(path, init = {}) {
  let session = await loadSession()
  session = await refreshIfNeeded(session)
  if (!session) return { ok: false, error: 'not authenticated' }
  const base = await dmsBase()
  const res = await fetch(`${base}${path}`, {
    ...init,
    headers: {
      ...(init.headers || {}),
      Authorization: `Bearer ${session.access_token}`,
    },
  })
  const body = res.headers.get('content-type')?.includes('application/json')
    ? await res.json()
    : await res.text()
  return { ok: res.ok, status: res.status, body }
}

// ---- Upload chain --------------------------------------------------

async function uploadFile({ name, mimeType, sizeBytes, sha256, bytesBase64 }) {
  // 1. initiate
  const init = await apiCall('/api/v1/storage/uploads/initiate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      filename: name,
      mime_type: mimeType,
      size_bytes: sizeBytes,
      sha256_hash: sha256,
    }),
  })
  if (!init.ok) return { ok: false, error: 'initiate failed', detail: init }

  // 2. PUT bytes to presigned URL
  const bytes = Uint8Array.from(atob(bytesBase64), (c) => c.charCodeAt(0))
  const putRes = await fetch(init.body.upload_url, {
    method: 'PUT',
    headers: { 'Content-Type': mimeType },
    body: bytes,
  })
  if (!putRes.ok) return { ok: false, error: 'put failed', status: putRes.status }

  // 3. complete
  const complete = await apiCall(
    `/api/v1/storage/uploads/${init.body.upload_id}/complete`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ checksum_sha256: sha256, size_bytes: sizeBytes }),
    },
  )
  if (!complete.ok) return { ok: false, error: 'complete failed', detail: complete }

  // 4. create document + version (Phase 1: into user's default workspace
  //    discovered via /api/v1/workspaces; Phase 2 allows per-upload picker)
  const ws = await apiCall('/api/v1/workspaces?limit=1')
  const workspaceId = ws.ok && ws.body?.workspaces?.[0]?.id
  if (!workspaceId) return { ok: false, error: 'no default workspace' }

  const doc = await apiCall('/api/v1/documents', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      workspace_id: workspaceId,
      title: name,
      mime_type: mimeType,
      content_blob_id: complete.body.content_blob_id,
      size_bytes: sizeBytes,
    }),
  })
  if (!doc.ok) return { ok: false, error: 'create document failed', detail: doc }

  await chrome.notifications.create({
    type: 'basic',
    iconUrl: 'icons/icon-128.png',
    title: 'Uploaded to VaultDMS',
    message: name,
  })
  return { ok: true, documentId: doc.body.id }
}

// ---- Web clipping --------------------------------------------------

async function clipActiveTab() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true })
  if (!tab?.id) return { ok: false, error: 'no active tab' }

  // Chromium: tabs.printToPDF returns the PDF directly. Firefox path
  // (captureVisibleTab + jsPDF) is in clipping.js as a fallback we
  // dynamically import only when printToPDF is unavailable.
  if (chrome.tabs.printToPDF) {
    const pdfData = await chrome.tabs.printToPDF({ tabId: tab.id })
    const sha256 = await sha256Hex(pdfData)
    return uploadFile({
      name: (tab.title || 'page') + '.pdf',
      mimeType: 'application/pdf',
      sizeBytes: pdfData.byteLength,
      sha256,
      bytesBase64: arrayBufferToBase64(pdfData),
    })
  }
  return { ok: false, error: 'printToPDF unavailable; Firefox fallback not yet ported into Phase 1' }
}

async function sha256Hex(buf) {
  const hash = await crypto.subtle.digest('SHA-256', buf)
  return Array.from(new Uint8Array(hash), (b) => b.toString(16).padStart(2, '0')).join('')
}

function arrayBufferToBase64(buf) {
  let s = ''
  const bytes = new Uint8Array(buf)
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i])
  return btoa(s)
}
