// Word task pane — two modes, dispatched by ?action= on the URL.
//
//   ?action=open  → search + recents + open-into-Word
//   ?action=save  → save the current document as a new version on
//                    a document the user picks (or, if the document
//                    was originally opened from SeDoc, defaults
//                    to that document — we store the source
//                    document_id in CustomProperties as a hint).
//
// Defaulting between the two when no ?action= is present:
//   * If Word has an open document: assume Save.
//   * Otherwise: Open.
// In practice the two ribbon buttons always pass the query string;
// the heuristic only matters if a user reloads the pane.
import { useEffect, useMemo, useState } from 'react'
import {
  Button, Input, Spinner, Tab, TabList,
  MessageBar, MessageBarBody, MessageBarTitle,
} from '@fluentui/react-components'

import {
  searchDocuments, listVersions, getDownloadURL,
  initiateUpload, putPresigned, completeUpload, createVersion,
  type SearchHit,
} from '../api'
import { readDocumentBytes, openWordURL } from '../wordIO'

declare const Office: {
  context: {
    document?: {
      url?: string
      settings?: {
        get: (key: string) => unknown
        set: (key: string, value: unknown) => void
        saveAsync: (cb: (r: { status: string; error?: { message: string } }) => void) => void
      }
    }
  }
}

type Mode = 'open' | 'save'

function deriveMode(): Mode {
  const q = new URLSearchParams(location.search).get('action')
  if (q === 'open' || q === 'save') return q
  return Office.context.document?.url ? 'save' : 'open'
}

const DOCX_MIME = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'

export function TaskPane() {
  const initial = useMemo(deriveMode, [])
  const [mode, setMode] = useState<Mode>(initial)
  return (
    <div style={{ padding: 16, height: '100%', boxSizing: 'border-box' }}>
      <TabList selectedValue={mode} onTabSelect={(_, d) => setMode(d.value as Mode)}>
        <Tab value="open">Open from SeDoc</Tab>
        <Tab value="save">Save to SeDoc</Tab>
      </TabList>
      <div style={{ marginTop: 16 }}>
        {mode === 'open' ? <OpenPanel /> : <SavePanel />}
      </div>
    </div>
  )
}

// =====================================================================
// Open
// =====================================================================

function OpenPanel() {
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  // On first mount, kick off a no-query search so the user sees
  // recent documents without typing.
  useEffect(() => {
    let cancelled = false
    setBusy(true)
    searchDocuments('')
      .then((r) => { if (!cancelled) { setHits(r); setBusy(false) } })
      .catch((e: Error) => { if (!cancelled) { setErr(e.message); setBusy(false) } })
    return () => { cancelled = true }
  }, [])

  const search = async () => {
    setErr(null); setBusy(true)
    try { setHits(await searchDocuments(q)) }
    catch (e) { setErr((e as Error).message) }
    finally   { setBusy(false) }
  }

  const open = async (h: SearchHit) => {
    setErr(null); setBusy(true)
    try {
      // Resolve the latest version's presigned URL. The list returns
      // newest first per the document service contract.
      const versions = await listVersions(h.document_id)
      if (versions.length === 0) {
        throw new Error('Document has no versions yet')
      }
      const url = await getDownloadURL(h.document_id, versions[0].id)
      // Stamp the source document_id into Office's CustomProperties
      // so the Save panel can default to "save back to the doc the
      // user opened" without a second search step.
      stampSourceDocumentID(h.document_id)
      openWordURL(url)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      {err && (
        <MessageBar intent="error">
          <MessageBarBody>{err}</MessageBarBody>
        </MessageBar>
      )}
      <div style={{ display: 'flex', gap: 8 }}>
        <Input
          value={q}
          placeholder="Search SeDoc…"
          onChange={(_, d) => setQ(d.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') void search() }}
          style={{ flex: 1 }}
        />
        <Button onClick={() => void search()} disabled={busy}>Search</Button>
      </div>

      {hits === null ? (
        busy ? <Spinner size="extra-tiny" /> : null
      ) : hits.length === 0 ? (
        <div style={{ color: '#666', fontSize: 13 }}>No documents match.</div>
      ) : (
        <ul style={{ listStyle: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
          {hits.map((h) => (
            <li key={h.document_id}>
              <Button
                appearance="subtle"
                onClick={() => void open(h)}
                style={{
                  width: '100%',
                  justifyContent: 'flex-start',
                  textAlign: 'start',
                  padding: '8px 10px',
                  height: 'auto',
                }}
              >
                <div>
                  <div style={{ fontWeight: 600 }}>{h.title || '(untitled)'}</div>
                  <div style={{ fontSize: 11, color: '#666' }}>
                    {[h.workspace_name, h.mime_type].filter(Boolean).join(' · ')}
                  </div>
                </div>
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

// =====================================================================
// Save
// =====================================================================

function SavePanel() {
  const [docID, setDocID] = useState<string>(() => readSourceDocumentID() ?? '')
  const [search, setSearch] = useState('')
  const [matches, setMatches] = useState<SearchHit[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [done, setDone] = useState(false)
  const [changeSummary, setChangeSummary] = useState('')

  const lookup = async () => {
    setErr(null); setBusy(true)
    try { setMatches(await searchDocuments(search)) }
    catch (e) { setErr((e as Error).message) }
    finally   { setBusy(false) }
  }

  const save = async () => {
    if (!docID) { setErr('Pick a target document first'); return }
    setErr(null); setBusy(true)
    try {
      // 1. Grab .docx bytes from Word.
      const bytes = await readDocumentBytes()
      const filename = guessFilename() + '.docx'
      // 2. Initiate upload → presigned PUT URL.
      const session = await initiateUpload({
        filename,
        mime_type:  DOCX_MIME,
        size_bytes: bytes.byteLength,
        document_id: docID,
      })
      // 2.a. Dedup hit short-circuits the PUT.
      let blobID = session.content_blob_id ?? session.existing_blob_id
      if (!session.deduplicated) {
        // 3. PUT bytes to MinIO.
        await putPresigned(session.presigned_put_url, bytes, DOCX_MIME)
        // 4. Commit → get blob_id.
        const completed = await completeUpload(session.upload_id)
        blobID = completed.content_blob_id ?? blobID
      }
      if (!blobID) throw new Error('storage did not return a blob id')
      // 5. New version on the target document.
      const summary = changeSummary.trim() || 'Saved from Microsoft Word add-in'
      await createVersion(docID, blobID, summary)
      setDone(true)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  if (done) {
    return (
      <div>
        <MessageBar intent="success">
          <MessageBarBody>
            <MessageBarTitle>New version saved</MessageBarTitle>
            SeDoc will OCR + classify the new bytes in the background.
          </MessageBarBody>
        </MessageBar>
        <Button style={{ marginTop: 16 }} onClick={() => { setDone(false); setChangeSummary('') }}>Save another version</Button>
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      {err && (
        <MessageBar intent="error">
          <MessageBarBody>{err}</MessageBarBody>
        </MessageBar>
      )}

      {docID ? (
        <div style={{ fontSize: 13, color: '#444' }}>
          Saving as new version of <code>{docID}</code>.
          <Button appearance="transparent" size="small" onClick={() => setDocID('')} style={{ marginInlineStart: 8 }}>
            change
          </Button>
        </div>
      ) : (
        <>
          <div style={{ display: 'flex', gap: 8 }}>
            <Input
              value={search}
              placeholder="Find target document…"
              onChange={(_, d) => setSearch(d.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') void lookup() }}
              style={{ flex: 1 }}
            />
            <Button onClick={() => void lookup()} disabled={busy}>Search</Button>
          </div>
          {matches && (
            <ul style={{ listStyle: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
              {matches.map((m) => (
                <li key={m.document_id}>
                  <Button
                    appearance="subtle"
                    onClick={() => setDocID(m.document_id)}
                    style={{ width: '100%', justifyContent: 'flex-start', textAlign: 'start' }}
                  >
                    {m.title || '(untitled)'}
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}

      <Input
        placeholder="Change summary (optional)"
        value={changeSummary}
        onChange={(_, d) => setChangeSummary(d.value)}
      />

      <Button appearance="primary" onClick={() => void save()} disabled={!docID || busy}>
        {busy ? <Spinner size="extra-tiny" /> : 'Save as new version'}
      </Button>
    </div>
  )
}

// =====================================================================
// Office CustomProperties — remember the source document_id between
// the Open click and a later Save click in the same Word session.
// =====================================================================

const SOURCE_KEY = 'vaultdms.source_document_id'

function stampSourceDocumentID(id: string): void {
  const settings = Office.context.document?.settings
  if (!settings) return
  settings.set(SOURCE_KEY, id)
  settings.saveAsync(() => { /* best-effort */ })
}

function readSourceDocumentID(): string | null {
  const v = Office.context.document?.settings?.get(SOURCE_KEY)
  return typeof v === 'string' ? v : null
}

// Word doesn't expose the open document's display name to the
// task pane reliably across hosts — Office.context.document.url
// is undefined for blank docs and the protocol-handler open
// doesn't always populate it. We fall back to a timestamped
// filename, which is fine because the actual filename users see
// in SeDoc comes from the version's display metadata, not the
// uploaded filename.
function guessFilename(): string {
  return `vaultdms-${new Date().toISOString().replace(/[:.]/g, '-')}`
}
