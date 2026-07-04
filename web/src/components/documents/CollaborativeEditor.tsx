// CollaborativeEditor — conflict-free (CRDT) plain-text editing surface for
// a (tenant, doc) room, backed by the same useYDoc + awareness + WS-auth +
// Postgres-snapshot chain as RealtimePresence (ADR 0096 / §17.4).
//
// Conflict-free by construction (Yjs Y.Text) — no merge dialogs. A
// monospace textarea is bound two-way to ytext('content'); remote carets
// are rendered from awareness as coloured bars + name tags positioned with
// runtime-measured char metrics. Read-only fallback when the user lacks
// `edit` on the document (the collaboration server enforces this too —
// this is the matching UI affordance, not the security boundary).
//
// No rich-editor dependency (only yjs + y-websocket are installed); inline
// caret placement is therefore monospace-grid based and best-effort across
// fonts. A Monaco/CodeMirror binding would give sub-pixel carets if we ever
// add the dep.
import { useCallback, useEffect, useRef, useState } from 'react'
import * as Y from 'yjs'
import { useQuery } from '@tanstack/react-query'
import { Wifi, WifiOff } from 'lucide-react'

import { useYDoc } from '@/lib/useYDoc'
import { useAuthStore } from '@/store/authStore'
import { checkPermission } from '@/api/permissions'

interface RemoteCursor {
  clientID: number
  name: string
  color: string
  index: number
}

const PALETTE = ['#3b82f6', '#10b981', '#f97316', '#8b5cf6', '#ec4899', '#06b6d4', '#eab308']

// Deterministic per-user colour — same hash/palette as RealtimePresence so a
// user is the same colour everywhere without server coordination.
function colorForUser(id: string): string {
  let h = 0
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0
  return PALETTE[h % PALETTE.length]
}

// Minimal-diff a textarea edit into Y.Text ops (common prefix + suffix) so
// concurrent edits from peers merge instead of clobbering.
function applyDiff(ydoc: Y.Doc, ytext: Y.Text, oldStr: string, newStr: string) {
  let start = 0
  const min = Math.min(oldStr.length, newStr.length)
  while (start < min && oldStr[start] === newStr[start]) start++
  let endOld = oldStr.length
  let endNew = newStr.length
  while (endOld > start && endNew > start && oldStr[endOld - 1] === newStr[endNew - 1]) {
    endOld--
    endNew--
  }
  ydoc.transact(() => {
    if (endOld > start) ytext.delete(start, endOld - start)
    if (endNew > start) ytext.insert(start, newStr.slice(start, endNew))
  })
}

export function CollaborativeEditor({ documentId }: { documentId: string }) {
  const handle = useYDoc(documentId)
  const me = useAuthStore((s) => s.user)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const [text, setText] = useState('')
  const [remotes, setRemotes] = useState<RemoteCursor[]>([])
  const [saving, setSaving] = useState(false)
  const [scroll, setScroll] = useState({ top: 0, left: 0 })
  const [metrics, setMetrics] = useState({ charW: 8.4, lineH: 21, pad: 12 })
  const savingTimer = useRef<number | null>(null)

  // `edit` permission gates writing. Default to read-only until confirmed
  // allowed — never let someone type into a doc they can't save.
  const { data: canEdit } = useQuery({
    queryKey: ['perm', 'document', documentId, 'edit'],
    queryFn: () => checkPermission('edit', 'document', documentId),
  })
  const readOnly = canEdit !== true

  // Measure real monospace metrics so the cursor overlay lines up with the
  // textarea regardless of the user's font.
  useEffect(() => {
    const ta = textareaRef.current
    if (!ta) return
    const cs = window.getComputedStyle(ta)
    const span = document.createElement('span')
    span.style.font = cs.font
    span.style.position = 'absolute'
    span.style.visibility = 'hidden'
    span.style.whiteSpace = 'pre'
    span.textContent = '0'.repeat(10)
    document.body.appendChild(span)
    const charW = span.getBoundingClientRect().width / 10
    document.body.removeChild(span)
    setMetrics({
      charW: charW || 8.4,
      lineH: parseFloat(cs.lineHeight) || 21,
      pad: parseFloat(cs.paddingTop) || 12,
    })
  }, [handle])

  // Y.Text -> textarea (uncontrolled, so local typing keeps its caret).
  // On remote edits we restore a clamped caret; precise caret mapping
  // through a remote delta would need an editor binding we don't have.
  useEffect(() => {
    if (!handle) return
    const ytext = handle.ydoc.getText('content')
    const ta = textareaRef.current
    if (ta) ta.value = ytext.toString()
    const observer = () => {
      const next = ytext.toString()
      setText(next)
      const el = textareaRef.current
      if (!el || el.value === next) return
      const s = el.selectionStart
      const e = el.selectionEnd
      el.value = next
      el.selectionStart = Math.min(s, next.length)
      el.selectionEnd = Math.min(e, next.length)
    }
    ytext.observe(observer)
    observer() // initial sync of the overlay's text model
    return () => ytext.unobserve(observer)
  }, [handle])

  const onChange = useCallback(
    (e: React.ChangeEvent<HTMLTextAreaElement>) => {
      if (!handle || readOnly) return
      const ytext = handle.ydoc.getText('content')
      applyDiff(handle.ydoc, ytext, ytext.toString(), e.target.value)
      setText(e.target.value)
      setSaving(true)
      if (savingTimer.current) window.clearTimeout(savingTimer.current)
      savingTimer.current = window.setTimeout(() => setSaving(false), 700)
    },
    [handle, readOnly],
  )

  // Publish identity once, and our caret on every selection change.
  useEffect(() => {
    if (!handle || !me) return
    handle.awareness.setLocalStateField('user', {
      id: me.id,
      name: me.display_name || me.email,
      email: me.email,
    })
    handle.awareness.setLocalStateField('color', colorForUser(me.id))
    return () => handle.awareness.setLocalStateField('user', null)
  }, [handle, me])

  const publishCursor = useCallback(() => {
    const ta = textareaRef.current
    if (!handle || !ta) return
    handle.awareness.setLocalStateField('cursor', { index: ta.selectionStart })
  }, [handle])

  // Remote awareness -> remote cursors.
  useEffect(() => {
    if (!handle) return
    const update = () => {
      const out: RemoteCursor[] = []
      handle.awareness.getStates().forEach((s, clientID) => {
        if (clientID === handle.awareness.clientID) return
        if (!s.user || !s.cursor) return
        out.push({
          clientID,
          name: s.user.name,
          color: s.color || '#6b7280',
          index: s.cursor.index,
        })
      })
      setRemotes(out)
    }
    update()
    handle.awareness.on('change', update)
    return () => handle.awareness.off('change', update)
  }, [handle])

  if (!handle) {
    return <div className="rounded-md border p-3 text-sm text-muted-foreground">Connecting to collaboration…</div>
  }

  // (row, col) of a character index in the current text.
  const rowCol = (index: number) => {
    const upto = text.slice(0, Math.min(index, text.length))
    const lastNl = upto.lastIndexOf('\n')
    const row = upto.length === 0 ? 0 : (upto.match(/\n/g) || []).length
    const col = lastNl === -1 ? upto.length : upto.length - lastNl - 1
    return { row, col }
  }

  return (
    <div className="rounded-md border" data-testid="collaborative-editor">
      <div className="flex items-center justify-between border-b px-3 py-1.5 text-xs">
        <SavedIndicator status={handle.status} saving={saving} readOnly={readOnly} />
        <PresencePile remotes={remotes} />
      </div>
      <div className="relative">
        <textarea
          ref={textareaRef}
          className="block h-72 w-full resize-none bg-transparent p-3 font-mono text-sm leading-[21px] outline-none"
          defaultValue=""
          readOnly={readOnly}
          spellCheck={false}
          onChange={onChange}
          onScroll={(e) => setScroll({ top: e.currentTarget.scrollTop, left: e.currentTarget.scrollLeft })}
          onSelect={publishCursor}
          onKeyUp={publishCursor}
          onClick={publishCursor}
          placeholder={readOnly ? 'Read-only — you don’t have edit access to this document' : 'Start typing… everyone here sees changes live'}
        />
        <div className="pointer-events-none absolute inset-0 overflow-hidden" aria-hidden>
          {remotes.map((r) => {
            const { row, col } = rowCol(r.index)
            const top = metrics.pad + row * metrics.lineH - scroll.top
            const left = metrics.pad + col * metrics.charW - scroll.left
            if (top < 0 || top > 288) return null // clip to the 18rem (h-72) box
            return (
              <div key={r.clientID} className="absolute" style={{ top, left }}>
                <div style={{ width: 2, height: metrics.lineH, background: r.color }} />
                <div
                  className="absolute whitespace-nowrap rounded px-1 text-[10px] leading-tight text-white"
                  style={{ background: r.color, top: -14 }}
                >
                  {r.name}
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

function SavedIndicator({
  status,
  saving,
  readOnly,
}: {
  status: 'connecting' | 'connected' | 'disconnected' | 'auth_failed'
  saving: boolean
  readOnly: boolean
}) {
  if (status !== 'connected') {
    const label = status === 'auth_failed' ? 'No access' : status === 'connecting' ? 'Connecting…' : 'Offline'
    return (
      <span className="flex items-center gap-1.5 text-muted-foreground">
        <WifiOff className="h-3.5 w-3.5" /> {label}
      </span>
    )
  }
  return (
    <span className="flex items-center gap-1.5 text-emerald-600">
      <Wifi className="h-3.5 w-3.5" />
      {readOnly ? 'Read-only' : saving ? 'Saving…' : 'Saved'}
    </span>
  )
}

function PresencePile({ remotes }: { remotes: RemoteCursor[] }) {
  if (remotes.length === 0) return <span className="text-muted-foreground">Just you</span>
  return (
    <div className="flex -space-x-2">
      {remotes.slice(0, 5).map((r) => (
        <span
          key={r.clientID}
          className="flex h-6 w-6 items-center justify-center rounded-full border-2 border-background text-[10px] font-medium text-white"
          style={{ backgroundColor: r.color }}
          title={r.name}
        >
          {(r.name.match(/\b\w/g) || []).slice(0, 2).join('').toUpperCase() || '?'}
        </span>
      ))}
      {remotes.length > 5 && <span className="ms-2 self-center text-muted-foreground">+{remotes.length - 5}</span>}
    </div>
  )
}
