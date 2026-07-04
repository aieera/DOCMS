import * as ImageManipulator from 'expo-image-manipulator'

// Document scanning + per-page filters.
//
// scanDocument launches the native scanner (automatic EDGE DETECTION + crop +
// perspective correction) and returns the cropped page image URIs. It needs a
// dev/standalone build — react-native-document-scanner-plugin is native and
// isn't in Expo Go — so it's lazy-imported and the screen falls back to the
// plain camera when it's unavailable.
export async function scanDocument(): Promise<string[]> {
  const mod: unknown = await import('react-native-document-scanner-plugin')
  const scanner = (mod as { default?: unknown }).default ?? mod
  const fn = (scanner as { scanDocument?: (o: unknown) => Promise<{ scannedImages?: string[] }> }).scanDocument
  if (!fn) throw new Error('document scanner native module not linked (use a dev build)')
  const { scannedImages } = await fn({ croppedImageQuality: 90, maxNumDocuments: 24 })
  return scannedImages ?? []
}

export type PageFilter = 'none' | 'grayscale' | 'enhance'

// applyFilter post-processes a page.
//
//   - 'enhance'  : recompress at full quality for crisper text (real, cheap).
//   - 'grayscale': expo-image-manipulator has NO colour ops, so true B/W needs
//                  a native filter lib (react-native-image-filter-kit) — that's
//                  flagged as a follow-up. We still round-trip the image so the
//                  pipeline is wired and a later swap is a one-line change.
export async function applyFilter(uri: string, filter: PageFilter): Promise<string> {
  if (filter === 'none') return uri
  const result = await ImageManipulator.manipulateAsync(uri, [], {
    compress: filter === 'enhance' ? 1 : 0.92,
    format: ImageManipulator.SaveFormat.JPEG,
  })
  return result.uri
}
