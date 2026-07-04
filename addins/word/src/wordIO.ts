// Word.js I/O helpers — read the open document as raw .docx bytes,
// open a URL into Word.
//
// Why getFileAsync + slices and not Word.run/ getOoxml?
//   * `Word.run(ctx => ctx.document.body.getOoxml())` returns the
//     WORDPROCESSINGML XML for the body of the document — NOT a
//     .docx archive. You can't write that back as a new version
//     because the round-trip loses headers, footers, styles, embedded
//     media, etc.
//   * `Office.context.document.getFileAsync(Office.FileType.Compressed,…)`
//     returns the full .docx as a sliced binary stream. That's the
//     surface we need.
//
// The slice API caps each slice at ~4 MB. We read every slice in
// sequence, concatenate into a single Uint8Array, then surface that
// to the caller. For very large documents (>100 MB) we'd want a
// streaming-PUT path; today the presigned PUT takes the whole
// buffer.

declare const Office: {
  context: {
    document: {
      getFileAsync: (
        type: number,
        opts: { sliceSize?: number },
        cb: (r: { status: 'succeeded' | 'failed'; value: OfficeFile; error?: { message: string } }) => void,
      ) => void
    }
  }
  FileType: { Compressed: number; Text: number; Pdf: number }
  AsyncResultStatus: { Succeeded: 'succeeded' }
}

// Minimal structural typing for the Word.run surface we touch — we don't
// pull in the full Word namespace from @types/office-js in this file.
interface WordRange {
  insertText: (text: string, location: string) => WordRange
  hyperlink: string
}
declare const Word: {
  run: (batch: (context: { document: { getSelection: () => WordRange }; sync: () => Promise<void> }) => Promise<void>) => Promise<void>
}

interface OfficeFile {
  size:        number
  sliceCount:  number
  getSliceAsync: (
    index: number,
    cb: (r: { status: 'succeeded' | 'failed'; value: { data: Uint8Array | number[]; index: number; size: number }; error?: { message: string } }) => void,
  ) => void
  closeAsync:  (cb: (r: { status: string }) => void) => void
}

const SLICE_SIZE = 4 * 1024 * 1024 // 4 MB — Office's documented cap.

/**
 * Capture the current document's .docx bytes. Throws on any Office.js
 * error; the returned Uint8Array is ready to PUT to a presigned URL.
 */
export async function readDocumentBytes(): Promise<Uint8Array> {
  const file = await new Promise<OfficeFile>((resolve, reject) => {
    Office.context.document.getFileAsync(
      Office.FileType.Compressed,
      { sliceSize: SLICE_SIZE },
      (r) => {
        if (r.status === 'succeeded') resolve(r.value)
        else reject(new Error(r.error?.message ?? 'getFileAsync failed'))
      },
    )
  })
  try {
    const slices: Uint8Array[] = []
    for (let i = 0; i < file.sliceCount; i++) {
      const slice = await new Promise<Uint8Array>((resolve, reject) => {
        file.getSliceAsync(i, (r) => {
          if (r.status !== 'succeeded') {
            reject(new Error(r.error?.message ?? `getSliceAsync ${i} failed`))
            return
          }
          const data = r.value.data
          // Office.js historically returned number[] in some hosts.
          // Normalise to Uint8Array regardless.
          resolve(Array.isArray(data) ? Uint8Array.from(data) : data)
        })
      })
      slices.push(slice)
    }
    // Concat slices into a single buffer.
    let total = 0
    for (const s of slices) total += s.byteLength
    const out = new Uint8Array(total)
    let off = 0
    for (const s of slices) {
      out.set(s, off)
      off += s.byteLength
    }
    return out
  } finally {
    // ALWAYS close the file handle, even when reads errored.
    // Without this Office accumulates handle leaks and starts
    // refusing future getFileAsync calls in the same session.
    await new Promise<void>((resolve) => file.closeAsync(() => resolve()))
  }
}

/**
 * Trigger the Word desktop / web client to open a URL.
 *
 * For Word desktop on Windows / Mac the `ms-word:ofe|u|<url>`
 * protocol handler is the canonical "Office Open by URL" surface —
 * supported since Word 2016. On the web, the protocol handler may
 * fall through to a regular href open, which is fine because the
 * URL itself resolves to the .docx content-type and the browser
 * hands the file off to Word for Web automatically.
 */
/**
 * Insert `text` as a hyperlink to `url` at the current selection,
 * replacing any selected text. Used by the Insert-link panel to drop a
 * reference to a SeDoc document into the open Word document.
 */
export async function insertHyperlink(url: string, text: string): Promise<void> {
  await Word.run(async (context) => {
    const range = context.document.getSelection().insertText(text, 'Replace')
    range.hyperlink = url
    await context.sync()
  })
}

export function openWordURL(url: string): void {
  // Encode the URL once for the protocol handler. ofe|u| is the
  // "Open for Editing | URL" prefix; `nft|u|` would be "New From
  // Template". We always want OFE so changes save back to the
  // same blob.
  const proto = `ms-word:ofe|u|${url}`
  // Office add-ins are sandboxed iframes; window.open can be
  // blocked by some Office hosts. Falling back to a transient
  // anchor click works in every supported host we've tested.
  const a = document.createElement('a')
  a.href = proto
  a.rel  = 'noopener'
  a.target = '_self'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
}
