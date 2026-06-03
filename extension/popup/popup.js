// SeDoc popup. Talks to background.js for everything that needs
// auth state, the network, or chrome.* privileges the popup itself
// doesn't hold under MV3's split context.
const signedOut = document.getElementById('signed-out')
const signedIn = document.getElementById('signed-in')
const search = document.getElementById('search')
const results = document.getElementById('results')
const status = document.getElementById('status')
const fileInput = document.getElementById('file')

init()

async function init() {
  const session = await msg({ type: 'session' })
  if (session?.session) {
    signedIn.hidden = false
    search.focus()
  } else {
    signedOut.hidden = false
  }
}

document.getElementById('sign-in')?.addEventListener('click', async () => {
  const r = await msg({ type: 'login' })
  if (r?.ok && r.url) {
    // Open the OAuth tab; the callback page closes itself on success
    // and updates session via background.
    chrome.tabs.create({ url: r.url })
    window.close()
  } else {
    status.textContent = `sign-in failed: ${r?.error || ''}`
  }
})

document.getElementById('sign-out')?.addEventListener('click', async () => {
  await msg({ type: 'logout' })
  signedIn.hidden = true
  signedOut.hidden = false
})

// Debounced search-as-you-type. Calls the search service through the
// background worker so the Bearer token never leaves the worker.
let searchTimer
search?.addEventListener('input', () => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(runSearch, 220)
})

async function runSearch() {
  const q = search.value.trim()
  if (!q) {
    results.innerHTML = ''
    return
  }
  status.textContent = 'searching…'
  const r = await msg({
    type: 'api',
    path: `/api/v1/search?q=${encodeURIComponent(q)}&limit=10`,
  })
  status.textContent = ''
  if (!r?.ok) {
    status.textContent = `search failed (${r?.status})`
    return
  }
  const hits = r.body?.hits || []
  results.innerHTML = ''
  for (const h of hits) {
    const li = document.createElement('li')
    li.innerHTML = `<div>${escape(h.title || '(untitled)')}</div>
      <div class="meta">${escape(h.workspace_name || '')} · ${escape(h.mime_type || '')}</div>`
    li.addEventListener('click', () => openDoc(h))
    results.appendChild(li)
  }
}

function openDoc(h) {
  if (!h.workspace_id || !h.document_id) return
  chrome.runtime.sendMessage({ type: 'session' }).then(async () => {
    // Open the doc detail page in a new tab pointing at the configured
    // DMS base. The base URL lives in chrome.storage.local.dms_base.
    const { dms_base } = await chrome.storage.local.get('dms_base')
    const base = dms_base || 'http://localhost:3000'
    chrome.tabs.create({
      url: `${base}/workspaces/${h.workspace_id}/documents/${h.document_id}`,
    })
  })
}

document.getElementById('clip')?.addEventListener('click', async () => {
  status.textContent = 'capturing page…'
  const r = await msg({ type: 'clip' })
  status.textContent = r?.ok ? 'clipped ✓' : `clip failed: ${r?.error || ''}`
})

document.getElementById('upload')?.addEventListener('click', () => fileInput.click())
fileInput?.addEventListener('change', async () => {
  const f = fileInput.files?.[0]
  if (!f) return
  status.textContent = `uploading ${f.name}…`
  const buf = await f.arrayBuffer()
  const sha256 = await sha256Hex(buf)
  const r = await msg({
    type: 'upload',
    file: {
      name: f.name,
      mimeType: f.type || 'application/octet-stream',
      sizeBytes: f.size,
      sha256,
      bytesBase64: arrayBufferToBase64(buf),
    },
  })
  status.textContent = r?.ok ? `uploaded ✓` : `upload failed: ${r?.error || ''}`
})

function msg(m) { return chrome.runtime.sendMessage(m) }

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

function escape(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]))
}
