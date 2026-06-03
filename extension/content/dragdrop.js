// Drag-drop content script. Loaded on user activation (activeTab +
// scripting), not via a content_scripts.matches block — so the
// extension is invisible until the user clicks the toolbar icon and
// chooses "Enable drag-drop on this tab" via the popup.
//
// Phase 1: a single floating overlay that pops up on dragenter and
// uploads on drop. Phase 2 adds the Gmail/Outlook-specific selectors.

let overlay
let dragDepth = 0

window.addEventListener('dragenter', (e) => {
  if (!isFileDrag(e)) return
  dragDepth++
  showOverlay()
}, true)

window.addEventListener('dragleave', () => {
  dragDepth = Math.max(0, dragDepth - 1)
  if (dragDepth === 0) hideOverlay()
}, true)

window.addEventListener('dragover', (e) => {
  if (!isFileDrag(e)) return
  e.preventDefault()
}, true)

window.addEventListener('drop', async (e) => {
  if (!isFileDrag(e)) return
  e.preventDefault()
  dragDepth = 0
  hideOverlay()
  const files = Array.from(e.dataTransfer?.files || [])
  for (const f of files) await uploadOne(f)
}, true)

function isFileDrag(e) {
  return Array.from(e.dataTransfer?.types || []).includes('Files')
}

function showOverlay() {
  if (overlay) return
  overlay = document.createElement('div')
  overlay.id = '__vaultdms_dropzone'
  overlay.textContent = 'Drop to upload to SeDoc'
  Object.assign(overlay.style, {
    position: 'fixed',
    inset: '24px',
    background: 'rgba(15, 23, 42, 0.85)',
    color: 'white',
    fontFamily: 'system-ui, sans-serif',
    fontSize: '20px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    zIndex: 2147483647,
    border: '3px dashed #60a5fa',
    borderRadius: '12px',
    pointerEvents: 'none',
  })
  document.documentElement.appendChild(overlay)
}

function hideOverlay() {
  overlay?.remove()
  overlay = undefined
}

async function uploadOne(file) {
  const buf = await file.arrayBuffer()
  const sha256 = await sha256Hex(buf)
  await chrome.runtime.sendMessage({
    type: 'upload',
    file: {
      name: file.name,
      mimeType: file.type || 'application/octet-stream',
      sizeBytes: file.size,
      sha256,
      bytesBase64: arrayBufferToBase64(buf),
    },
  })
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
