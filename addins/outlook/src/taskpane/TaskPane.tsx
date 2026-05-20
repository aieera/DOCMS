// Save to VaultDMS — task pane UI (ADR 0112).
//
// Layout (top to bottom):
//   * Email summary line (subject + from). Helps the user confirm
//     they're saving the right thread before they click Save.
//   * Workspace dropdown (lazy-loaded once on first open).
//   * Folder dropdown (depends on the workspace).
//   * Tag input (comma-separated).
//   * "Include attachments" checkbox (default ON).
//   * Save button. Disabled until a workspace+folder is picked.
//
// What the Save handler does:
//   1. Pull body + attachments from the open message via Office.js.
//   2. POST to /api/v1/integrations/m365/ingest-email.
//   3. On 2xx: toast + close the pane.
//   4. On 4xx/5xx: surface the error inline; don't close so the
//      user can retry without re-picking the workspace.
//
// Why not stream attachments?
//   Office.context.mailbox.item.getAttachmentContentAsync returns
//   base64. There's no streaming API; the entire payload sits in
//   memory before the POST. For one-off "save this email" volumes
//   that's fine — a 25 MB attachment is ~33 MB base64 + the body,
//   well within fetch() limits. Bulk-ingest (entire folder at
//   once) is its own surface and not in this prompt's scope.

import { useEffect, useMemo, useState } from 'react'
import {
  Button, Combobox, Option, Field, Input, Switch, Spinner,
  MessageBar, MessageBarBody, MessageBarTitle,
} from '@fluentui/react-components'

import {
  listWorkspaces, listFolders, ingestEmail,
  type Workspace, type Folder, type IngestAttachment,
} from '../api'

// Office.js types — we don't pull in @types/office-js entirely
// because it adds 200 KB to the bundle and we only touch a handful
// of surfaces. The structural typing covers what we use.
declare const Office: {
  context: {
    mailbox: {
      item?: {
        subject:    string
        from:       { emailAddress: string; displayName?: string }
        to:         { emailAddress: string }[]
        dateTimeCreated: Date
        internetMessageId: string
        body: {
          getAsync(coercion: 'html' | 'text', cb: (r: AsyncResult<string>) => void): void
        }
        getAttachmentsAsync(cb: (r: AsyncResult<AttachmentMeta[]>) => void): void
        getAttachmentContentAsync(
          id: string,
          opts: { asyncContext?: unknown },
          cb: (r: AsyncResult<AttachmentContent>) => void,
        ): void
      }
    }
  }
}

interface AsyncResult<T> {
  status: 'succeeded' | 'failed'
  value:  T
  error?: { message: string }
}

interface AttachmentMeta {
  id:          string
  name:        string
  size:        number
  contentType: string
  attachmentType: 'file' | 'item' | 'cloud'
}

interface AttachmentContent {
  format:  string
  content: string // base64
}

export function TaskPane() {
  const item = Office.context.mailbox.item

  // ---- pickers ---------------------------------------------------
  const [workspaces, setWorkspaces] = useState<Workspace[] | null>(null)
  const [workspaceID, setWorkspaceID] = useState<string>('')
  const [folders, setFolders] = useState<Folder[] | null>(null)
  const [folderID, setFolderID] = useState<string>('')
  const [tagsText, setTagsText] = useState('')
  const [includeAttachments, setIncludeAttachments] = useState(true)

  // ---- save flow -------------------------------------------------
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [done, setDone] = useState(false)

  // Initial workspace fetch — runs once when Office.onReady has
  // fired (the outer index.tsx ensures we don't mount before then).
  useEffect(() => {
    let cancelled = false
    listWorkspaces()
      .then((rows) => {
        if (cancelled) return
        setWorkspaces(rows)
        if (rows.length === 1) setWorkspaceID(rows[0].id)
      })
      .catch((e: Error) => setErr(e.message))
    return () => { cancelled = true }
  }, [])

  // Folder fetch depends on the selected workspace.
  useEffect(() => {
    if (!workspaceID) {
      setFolders(null)
      setFolderID('')
      return
    }
    let cancelled = false
    setFolders(null)
    setFolderID('')
    listFolders(workspaceID)
      .then((rows) => {
        if (cancelled) return
        setFolders(rows)
        // Auto-select the workspace root if exactly one folder.
        if (rows.length === 1) setFolderID(rows[0].id)
      })
      .catch((e: Error) => setErr(e.message))
    return () => { cancelled = true }
  }, [workspaceID])

  const subject  = item?.subject ?? '(no subject)'
  const fromAddr = item?.from?.emailAddress ?? ''
  const canSave  = !!workspaceID && !!folderID && !saving

  const tags = useMemo(
    () => tagsText.split(',').map((t) => t.trim()).filter(Boolean),
    [tagsText],
  )

  const onSave = async () => {
    if (!item) return
    setSaving(true)
    setErr(null)
    try {
      const bodyHTML = await readBodyAsync(item, 'html')
      const bodyText = await readBodyAsync(item, 'text')
      const attachments = includeAttachments ? await readAttachments(item) : []
      const res = await ingestEmail({
        subject,
        from:         fromAddr,
        to:           (item.to ?? []).map((t) => t.emailAddress),
        sent_at:      item.dateTimeCreated?.toISOString?.() ?? new Date().toISOString(),
        body_html:    bodyHTML,
        body_text:    bodyText,
        attachments,
        workspace_id: workspaceID,
        folder_id:    folderID,
        tags,
        message_id:   item.internetMessageId,
      })
      setDone(true)
      console.info('saved', res)
    } catch (e) {
      setErr((e as Error).message ?? 'Save failed')
    } finally {
      setSaving(false)
    }
  }

  if (!item) {
    return <div style={{ padding: 16 }}>Open a message to use this add-in.</div>
  }

  if (done) {
    return (
      <div style={{ padding: 16 }}>
        <MessageBar intent="success">
          <MessageBarBody>
            <MessageBarTitle>Saved to VaultDMS</MessageBarTitle>
            Email + attachments have been queued for ingestion. OCR + classification will run in the background.
          </MessageBarBody>
        </MessageBar>
        <Button style={{ marginTop: 16 }} onClick={() => setDone(false)}>Save another</Button>
      </div>
    )
  }

  return (
    <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 12 }}>
      <header>
        <div style={{ fontSize: 16, fontWeight: 600, marginBottom: 2 }}>{subject}</div>
        <div style={{ fontSize: 12, color: '#666' }}>From {fromAddr}</div>
      </header>

      {err && (
        <MessageBar intent="error">
          <MessageBarBody>{err}</MessageBarBody>
        </MessageBar>
      )}

      <Field label="Workspace">
        {workspaces === null ? (
          <Spinner size="extra-tiny" />
        ) : (
          <Combobox
            value={workspaces.find((w) => w.id === workspaceID)?.name ?? ''}
            placeholder="Pick a workspace…"
            onOptionSelect={(_, data) => setWorkspaceID(String(data.optionValue ?? ''))}
          >
            {workspaces.map((w) => (
              <Option key={w.id} value={w.id}>{w.name}</Option>
            ))}
          </Combobox>
        )}
      </Field>

      <Field label="Folder">
        {workspaceID === '' ? (
          <Input disabled placeholder="Pick a workspace first" />
        ) : folders === null ? (
          <Spinner size="extra-tiny" />
        ) : folders.length === 0 ? (
          <Input disabled placeholder="This workspace has no folders" />
        ) : (
          <Combobox
            value={folders.find((f) => f.id === folderID)?.name ?? ''}
            placeholder="Pick a folder…"
            onOptionSelect={(_, data) => setFolderID(String(data.optionValue ?? ''))}
          >
            {folders.map((f) => (
              <Option key={f.id} value={f.id}>{f.name}</Option>
            ))}
          </Combobox>
        )}
      </Field>

      <Field label="Tags (comma-separated)">
        <Input value={tagsText} onChange={(_, d) => setTagsText(d.value)} placeholder="invoice, q1-2026" />
      </Field>

      <Switch
        checked={includeAttachments}
        onChange={(_, d) => setIncludeAttachments(Boolean(d.checked))}
        label={`Include attachments`}
      />

      <Button appearance="primary" onClick={onSave} disabled={!canSave}>
        {saving ? <Spinner size="extra-tiny" /> : 'Save email'}
      </Button>
    </div>
  )
}

// ---- Office.js helpers --------------------------------------------

function readBodyAsync(
  item: NonNullable<typeof Office.context.mailbox.item>,
  coercion: 'html' | 'text',
): Promise<string> {
  return new Promise((resolve, reject) => {
    item.body.getAsync(coercion, (r) => {
      if (r.status === 'succeeded') resolve(r.value ?? '')
      else reject(new Error(r.error?.message ?? `body.${coercion} read failed`))
    })
  })
}

function readAttachments(
  item: NonNullable<typeof Office.context.mailbox.item>,
): Promise<IngestAttachment[]> {
  return new Promise((resolve, reject) => {
    item.getAttachmentsAsync((meta) => {
      if (meta.status !== 'succeeded') {
        reject(new Error(meta.error?.message ?? 'getAttachmentsAsync failed'))
        return
      }
      const tasks = (meta.value ?? [])
        .filter((a) => a.attachmentType === 'file')  // skip embedded "item" attachments + cloud links for v1
        .map((a) => readOne(item, a))
      Promise.all(tasks).then(resolve, reject)
    })
  })
}

function readOne(
  item: NonNullable<typeof Office.context.mailbox.item>,
  a: AttachmentMeta,
): Promise<IngestAttachment> {
  return new Promise((resolve, reject) => {
    item.getAttachmentContentAsync(a.id, {}, (r) => {
      if (r.status !== 'succeeded') {
        reject(new Error(r.error?.message ?? `attachment ${a.name} read failed`))
        return
      }
      resolve({
        name:        a.name,
        mime_type:   a.contentType,
        content_b64: r.value.content,
      })
    })
  })
}
