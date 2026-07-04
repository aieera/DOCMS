import * as FileSystem from 'expo-file-system'
import * as Print from 'expo-print'

// imagesToPdf renders the given local page images into a single multi-page PDF
// (one image per page, centred and fit) using the platform print engine, and
// returns the local PDF file URI + its byte size for the upload's size_bytes.
//
// expo-print is the most portable on-device PDF generator (no native linking
// beyond Expo). Each page is embedded as a base64 data-URI in a paginated HTML
// doc, so there's no server round-trip.
export async function imagesToPdf(imageUris: string[]): Promise<{ uri: string; size: number }> {
  if (imageUris.length === 0) throw new Error('no pages to render')

  const pages = await Promise.all(
    imageUris.map(async (uri) => {
      const b64 = await FileSystem.readAsStringAsync(uri, { encoding: FileSystem.EncodingType.Base64 })
      return (
        `<div style="page-break-after:always;display:flex;align-items:center;` +
        `justify-content:center;height:100vh;">` +
        `<img src="data:image/jpeg;base64,${b64}" style="max-width:100%;max-height:100%;"/></div>`
      )
    }),
  )

  const html = `<html><head><meta name="viewport" content="width=device-width"/></head>` +
    `<body style="margin:0;padding:0;">${pages.join('')}</body></html>`

  const { uri } = await Print.printToFileAsync({ html, base64: false })
  const info = await FileSystem.getInfoAsync(uri)
  return { uri, size: info.exists ? info.size ?? 0 : 0 }
}
