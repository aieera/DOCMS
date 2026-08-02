/** Save translated text as a .txt file. Shared by the viewer's header button
 * and the rail panel's per-row download so both produce identical filenames. */
export function downloadTranslationText(text: string, filename: string) {
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

/** `invoice-2026.pdf` + `ar` → `invoice-2026.ar.txt`. Strips the source
 * extension and anything a filesystem would reject. */
export function translationFilename(docTitle: string | undefined, targetLanguage: string) {
  const base = (docTitle ?? 'translation')
    .replace(/\.[^./\\]+$/, '')
    .replace(/[/\\?%*:|"<>]/g, '-')
    .trim()
  return `${base || 'translation'}.${targetLanguage}.txt`
}
