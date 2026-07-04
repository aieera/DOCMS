// Word task pane — three modes, dispatched by ?action= on the URL.
//
//   ?action=open   → search + recents + open-into-Word
//   ?action=save   → save the current document as a new version on a
//                     document the user explicitly picks. (No persisted
//                     "source document" default: the pane's
//                     CustomProperties belong to whatever document is
//                     currently open, so a stamp taken at Open time
//                     tags the wrong file.)
//   ?action=insert → insert a link/reference at the cursor.
//
// Defaulting between the two when no ?action= is present:
//   * If Word has an open document: assume Save.
//   * Otherwise: Open.
// In practice the two ribbon buttons always pass the query string;
// the heuristic only matters if a user reloads the pane.
import { useEffect, useMemo, useState } from 'react'
import {
  Button, Input, Spinner, Tab, TabList, Checkbox,
  MessageBar, MessageBarBody, MessageBarTitle,
} from '@fluentui/react-components'

import {
  searchDocuments, listVersions, getDownloadURL,
  initiateUpload, putPresigned, completeUpload, createVersion,
  createShareLink, documentURL, ConflictError,
  type SearchHit,
} from '../api'
import { readDocumentBytes, openWordURL, insertHyperlink } from '../wordIO'

declare const Office: {
  context: {
    document?: {
      url?: string
    }
  }
}

type Mode = 'open' | 'save' | 'insert'

function deriveMode(): Mode {
  const q = new URLSearchParams(location.search).get('action')
  if (q === 'open' || q === 'save' || q === 'insert') return q
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
        <Tab value="insert">Insert link</Tab>
      </TabList>
      <div style={{ marginTop: 16 }}>
        {mode === 'open' ? <OpenPanel /> : mode === 'save' ? <SavePanel /> : <InsertPanel />}
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
      // NOTE: no CustomProperties "source document" stamp here. The pane
      // runs in whatever document is CURRENTLY open — stamping before
      // openWordURL launches the download would tag the wrong document
      // and later default a save at a stale target/base (silent
      // wrong-target overwrite). Save always requires an explicit pick;
      // the base version is read from the head at save time.
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
  // Explicit pick every session (C4): a persisted default was stamped
  // into the browsing document's settings, not the opened file, so it
  // pointed saves at the wrong target.
  const [target, setTarget] = useState<SearchHit | null>(null)
  // Base for optimistic concurrency. Starts empty (head is read at
  // save time); advances to the created version after each save so a
  // second save in the same pane session doesn't false-conflict.
  const [baseVersionID, setBaseVersionID] = useState<string>('')
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
    if (!target) { setErr('Pick a target document first'); return }
    const docID = target.document_id
    setErr(null); setBusy(true)
    try {
      // 1. Grab .docx bytes from Word.
      const bytes = await readDocumentBytes()
      const filename = guessFilename() + '.docx'
      // 2. Initiate upload → presigned PUT URL. document_id scopes the
      //    permission check to the target; workspace_id rides along as
      //    the fallback scope.
      const session = await initiateUpload({
        filename,
        mime_type:  DOCX_MIME,
        size_bytes: bytes.byteLength,
        document_id: docID,
        workspace_id: target.workspace_id,
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
      // 5. New version on the target document, guarded by the base
      //    version. If we don't have a stamped base (the user picked a
      //    target they didn't open here), read the current head so the
      //    save still carries a base for the server's FOR UPDATE check.
      let base = baseVersionID
      if (!base) {
        const versions = await listVersions(docID)
        base = versions[0]?.id ?? ''
      }
      const summary = changeSummary.trim() || 'Saved from Microsoft Word add-in'
      const created = await createVersion(docID, blobID, summary, base || undefined)
      // Advance our local base to the version we just wrote so a second
      // save in the same session doesn't false-conflict against itself.
      if (created?.id) setBaseVersionID(created.id)
      setDone(true)
    } catch (e) {
      if (e instanceof ConflictError) {
        setErr('A newer version was saved to SeDoc since you opened this document. Open the latest version, reapply your edits, then save again.')
      } else {
        setErr((e as Error).message)
      }
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

      {target ? (
        <div style={{ fontSize: 13, color: '#444' }}>
          Saving as new version of <strong>{target.title || '(untitled)'}</strong>.
          <Button appearance="transparent" size="small" onClick={() => { setTarget(null); setBaseVersionID('') }} style={{ marginInlineStart: 8 }}>
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
                    onClick={() => { setTarget(m); setBaseVersionID('') }}
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

      <Button appearance="primary" onClick={() => void save()} disabled={!target || busy}>
        {busy ? <Spinner size="extra-tiny" /> : 'Save as new version'}
      </Button>
    </div>
  )
}

// =====================================================================
// Insert link — drop a reference to a SeDoc document into the open Word
// document at the cursor. Internal reference by default (opens with a
// SeDoc session); optionally a tokenised share link for external
// recipients.
// =====================================================================

function InsertPanel() {
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [picked, setPicked] = useState<SearchHit | null>(null)
  const [shareable, setShareable] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [done, setDone] = useState(false)

  const runSearch = async () => {
    setErr(null); setBusy(true)
    try { setHits(await searchDocuments(q)) }
    catch (e) { setErr((e as Error).message) }
    finally   { setBusy(false) }
  }

  const insert = async () => {
    if (!picked) { setErr('Pick a document first'); return }
    setErr(null); setBusy(true)
    try {
      let url: string
      if (shareable) {
        url = (await createShareLink(picked.document_id)).url
      } else {
        if (!picked.workspace_id) {
          throw new Error('This search result has no workspace — use a shareable link instead.')
        }
        url = documentURL(picked.workspace_id, picked.document_id)
      }
      await insertHyperlink(url, picked.title || 'SeDoc document')
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
            <MessageBarTitle>Link inserted</MessageBarTitle>
            A reference to {picked?.title || 'the document'} was inserted at your cursor.
          </MessageBarBody>
        </MessageBar>
        <Button style={{ marginTop: 16 }} onClick={() => { setDone(false); setPicked(null) }}>Insert another</Button>
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      {err && (
        <MessageBar intent="error"><MessageBarBody>{err}</MessageBarBody></MessageBar>
      )}
      {picked ? (
        <div style={{ fontSize: 13, color: '#444' }}>
          Linking to <strong>{picked.title || '(untitled)'}</strong>.
          <Button appearance="transparent" size="small" onClick={() => setPicked(null)} style={{ marginInlineStart: 8 }}>
            change
          </Button>
        </div>
      ) : (
        <>
          <div style={{ display: 'flex', gap: 8 }}>
            <Input
              value={q}
              placeholder="Find a document to link…"
              onChange={(_, d) => setQ(d.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') void runSearch() }}
              style={{ flex: 1 }}
            />
            <Button onClick={() => void runSearch()} disabled={busy}>Search</Button>
          </div>
          {hits && (
            <ul style={{ listStyle: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
              {hits.map((h) => (
                <li key={h.document_id}>
                  <Button
                    appearance="subtle"
                    onClick={() => setPicked(h)}
                    style={{ width: '100%', justifyContent: 'flex-start', textAlign: 'start' }}
                  >
                    {h.title || '(untitled)'}
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}

      <Checkbox
        checked={shareable}
        onChange={(_, d) => setShareable(d.checked === true)}
        label="Create a shareable link (anyone with the link can view)"
      />

      <Button appearance="primary" onClick={() => void insert()} disabled={!picked || busy}>
        {busy ? <Spinner size="extra-tiny" /> : 'Insert link'}
      </Button>
    </div>
  )
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
