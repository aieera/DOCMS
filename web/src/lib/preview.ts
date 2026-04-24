// Preview-service helpers. Centralised so DocumentCard + PDFViewer +
// any future viewer agree on URL shape and the "previewable" predicate.

export const PREVIEW_API_BASE = '/api/v1/previews'

export function pageThumbnailURL(documentId: string, pageNumber: number): string {
  return `${PREVIEW_API_BASE}/${documentId}/pages/${pageNumber}`
}

// MIME types the preview worker can thumbnail today. A `false` here
// means "preview will never appear" so the UI doesn't flash a
// "generating preview" chip for text/.md docs etc.
export function isPreviewable(mime: string | undefined): boolean {
  if (!mime) return false
  if (mime === 'application/pdf') return true
  if (mime.startsWith('image/')) return true
  // Office formats are queued for Wave 17 — when the preview worker
  // gains docx/pptx support, flip these to true here.
  return false
}
