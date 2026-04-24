// Thin wrapper around react-native-document-scanner-plugin. The
// plugin returns N pages; the caller decides whether to stitch or
// upload per-page. We normalise to a single-shot promise that
// yields the first-page local URI — matches the existing one-shot
// capture flow in upload.tsx.
//
// NOTE: this plugin is a native module and does NOT work in Expo Go.
// A dev client (EAS build) is required to exercise it end-to-end.

import DocumentScanner from 'react-native-document-scanner-plugin'

export interface CaptureResult {
  uri: string
  pages: number
}

export async function captureDocument(): Promise<CaptureResult | null> {
  const { scannedImages, status } = await DocumentScanner.scanDocument({
    maxNumDocuments: 1,           // one scan at a time
    responseType: 'imageFilePath', // file URI, not base64 — keep memory flat
    letUserAdjustCrop: true,       // user confirms the detected edges
  })
  if (status !== 'success' || !scannedImages?.length) return null
  return { uri: scannedImages[0], pages: scannedImages.length }
}
