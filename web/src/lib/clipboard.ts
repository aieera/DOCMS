/** Copy text to the clipboard, working on plain-HTTP origins too.
 *
 * navigator.clipboard exists only in secure contexts (HTTPS or localhost).
 * Test servers are routinely reached over http://<lan-ip>, where the API is
 * undefined and every copy button silently fails — so fall back to the
 * legacy hidden-textarea + execCommand path there. Throws when both paths
 * fail, so callers can keep their existing error toasts.
 */
export async function copyText(text: string): Promise<void> {
  if (typeof navigator !== 'undefined' && navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text)
    return
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  // Keep it rendered (execCommand needs a selectable node) but invisible
  // and out of the layout.
  ta.style.position = 'fixed'
  ta.style.top = '0'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.focus()
  ta.select()
  try {
    if (!document.execCommand('copy')) {
      throw new Error('execCommand("copy") returned false')
    }
  } finally {
    ta.remove()
  }
}
