// ADR 0066 — comments side panel. Threaded view with:
//   - Top-level comment list (ordered by created_at)
//   - Inline replies under each thread root
//   - Resolve / unresolve toggle on the root
//   - Emoji reaction picker (3 default emoji + a "+" to add custom)
//   - @mention autocomplete from /admin/users (debounced)
//   - Real-time updates: invalidates the comments query on any
//     dms.comment.* WS message for this document_id
//
// The component is intentionally self-contained: pass it a
// documentId and it manages its own queries + WS subscription.
import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import {
  MessageSquare, Send, Check, CheckCircle2, Reply, Trash2, Smile,
} from 'lucide-react'

import {
  listComments, createComment, replyToComment, deleteComment,
  resolveComment, unresolveComment,
  addReaction, removeReaction,
  parseBodyForRender, mentionToken,
  type Comment, type ReactionAggregate,
} from '@/api/comments'
import { getUsers } from '@/api/admin'
import { useAuthStore } from '@/store/authStore'
import { useWebSocket } from '@/hooks/useWebSocket'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { formatRelativeTime } from '@/lib/formatters'

const QUICK_EMOJI = ['👍', '🎉', '❤️', '👀']

export function CommentsPanel({ documentId }: { documentId: string }) {
  const qc = useQueryClient()
  const me = useAuthStore((s) => s.user)
  const [showResolved, setShowResolved] = useState(false)

  const { data: comments, isLoading } = useQuery({
    queryKey: ['comments', documentId, showResolved],
    queryFn: () => listComments(documentId, showResolved),
  })

  // Real-time: any dms.comment.* event for this document
  // invalidates the comments query. Reaction events ALSO need to
  // invalidate the per-comment reaction query — otherwise an emoji
  // added by another user shows up in the comment refetch but the
  // count badge stays stale until the user refocuses the tab.
  useWebSocket((data) => {
    const msg = data as { type?: string; data?: { document_id?: string; comment_id?: string } }
    if (!msg?.type?.startsWith('dms.comment.')) return
    if (msg.data?.document_id && msg.data.document_id !== documentId) return
    qc.invalidateQueries({ queryKey: ['comments', documentId] })
    if (msg.type === 'dms.comment.reaction.v1' && msg.data?.comment_id) {
      qc.invalidateQueries({ queryKey: ['comment-reactions', msg.data.comment_id] })
    }
  })

  // Group flat list into thread roots + replies.
  const threads = useMemo(() => groupThreads(comments ?? []), [comments])

  return (
    <aside className="flex h-full w-96 flex-col border-l border-[var(--color-border)] bg-[var(--color-bg-secondary)]">
      <header className="flex items-center justify-between border-b border-[var(--color-border)] px-4 py-3">
        <h2 className="flex items-center gap-2 text-sm font-semibold">
          <MessageSquare className="h-4 w-4" /> Comments
        </h2>
        <label className="flex items-center gap-1 text-xs">
          <input type="checkbox" checked={showResolved} onChange={(e) => setShowResolved(e.target.checked)} />
          Show resolved
        </label>
      </header>

      <div className="flex-1 overflow-y-auto p-4 space-y-4" data-testid="comments-list">
        {isLoading && <Spinner />}
        {threads.length === 0 && !isLoading && (
          <p className="text-sm text-[var(--color-text-secondary)]">No comments yet — be the first.</p>
        )}
        {threads.map((t) => (
          <ThreadCard key={t.root.id} thread={t} currentUserId={me?.id ?? ''} />
        ))}
      </div>

      <div className="border-t border-[var(--color-border)] p-3">
        <NewCommentForm documentId={documentId} />
      </div>
    </aside>
  )
}

// ---- thread card ---------------------------------------------------------

interface Thread {
  root: Comment
  replies: Comment[]
}

function groupThreads(comments: Comment[]): Thread[] {
  const byRoot = new Map<string, Thread>()
  for (const c of comments) {
    if (!c.parent_comment_id) byRoot.set(c.id, { root: c, replies: [] })
  }
  for (const c of comments) {
    if (c.parent_comment_id) {
      const t = byRoot.get(c.parent_comment_id)
      if (t) t.replies.push(c)
    }
  }
  return Array.from(byRoot.values()).sort((a, b) =>
    a.root.created_at.localeCompare(b.root.created_at),
  )
}

function ThreadCard({ thread, currentUserId }: { thread: Thread; currentUserId: string }) {
  const qc = useQueryClient()
  const [replying, setReplying] = useState(false)

  const toggleResolve = useMutation({
    mutationFn: () => (thread.root.is_resolved ? unresolveComment(thread.root.id) : resolveComment(thread.root.id)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['comments', thread.root.document_id] }),
  })

  return (
    <article
      className={`rounded-md border p-3 ${
        thread.root.is_resolved
          ? 'border-emerald-300 bg-emerald-50 dark:border-emerald-700 dark:bg-emerald-950/30'
          : 'border-[var(--color-border)] bg-[var(--color-bg)]'
      }`}
      data-testid={`thread-${thread.root.id}`}
    >
      <CommentBubble c={thread.root} currentUserId={currentUserId} />
      {thread.replies.length > 0 && (
        <div className="mt-2 space-y-2 border-l-2 border-[var(--color-border)] pl-3">
          {thread.replies.map((r) => (
            <CommentBubble key={r.id} c={r} currentUserId={currentUserId} />
          ))}
        </div>
      )}

      <div className="mt-2 flex items-center gap-2 text-xs">
        <button onClick={() => setReplying((v) => !v)} className="text-[var(--color-text-secondary)] hover:underline">
          <Reply className="mr-1 inline h-3 w-3" /> Reply
        </button>
        <button
          onClick={() => toggleResolve.mutate()}
          className={thread.root.is_resolved ? 'text-emerald-700' : 'text-[var(--color-text-secondary)] hover:underline'}
          data-testid={`resolve-${thread.root.id}`}
        >
          {thread.root.is_resolved ? (
            <><CheckCircle2 className="mr-1 inline h-3 w-3" /> Resolved — click to reopen</>
          ) : (
            <><Check className="mr-1 inline h-3 w-3" /> Mark resolved</>
          )}
        </button>
      </div>

      {replying && (
        <div className="mt-2">
          <ReplyForm parentId={thread.root.id} onSubmitted={() => setReplying(false)} />
        </div>
      )}
    </article>
  )
}

function CommentBubble({ c, currentUserId }: { c: Comment; currentUserId: string }) {
  const segments = parseBodyForRender(c.body)
  const qc = useQueryClient()
  const remove = useMutation({
    mutationFn: () => deleteComment(c.id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['comments', c.document_id] }),
    onError: (e: any) => toast.error(e?.response?.data?.error ?? 'failed'),
  })
  return (
    <div className="text-sm">
      <div className="flex items-baseline gap-2">
        <strong>{c.author_id.slice(0, 8)}</strong>
        <span className="text-xs text-[var(--color-text-secondary)]">{formatRelativeTime(c.created_at)}</span>
        {c.author_id === currentUserId && (
          <button
            onClick={() => remove.mutate()}
            aria-label="delete"
            className="ml-auto text-[var(--color-text-secondary)] hover:text-red-600"
          >
            <Trash2 className="h-3 w-3" />
          </button>
        )}
      </div>
      <p className="mt-1 whitespace-pre-wrap break-words">
        {segments.map((s, i) =>
          s.userId ? (
            <span key={i} className="rounded bg-blue-100 px-1 text-blue-800 dark:bg-blue-950/50 dark:text-blue-200">
              {s.text}
            </span>
          ) : (
            <span key={i}>{s.text}</span>
          ),
        )}
      </p>
      <ReactionBar commentId={c.id} />
    </div>
  )
}

// ---- reactions ----------------------------------------------------------

function ReactionBar({ commentId }: { commentId: string }) {
  const qc = useQueryClient()
  const me = useAuthStore((s) => s.user)
  const { data: reactions } = useQuery({
    queryKey: ['comment-reactions', commentId],
    queryFn: () => listReactionsViaListEndpoint(commentId),
    staleTime: 30_000,
  })
  const toggle = useMutation({
    mutationFn: async (emoji: string) => {
      const mineHas = reactions?.find((r) => r.emoji === emoji)?.users.includes(me?.id ?? '')
      if (mineHas) await removeReaction(commentId, emoji)
      else await addReaction(commentId, emoji)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['comment-reactions', commentId] })
    },
  })

  return (
    <div className="mt-1 flex flex-wrap items-center gap-1" data-testid={`reactions-${commentId}`}>
      {(reactions ?? []).map((r) => {
        const mine = me && r.users.includes(me.id)
        return (
          <button
            key={r.emoji}
            onClick={() => toggle.mutate(r.emoji)}
            className={`rounded-full border px-2 py-0.5 text-xs ${
              mine ? 'border-blue-400 bg-blue-50 dark:border-blue-600 dark:bg-blue-950/40' : 'border-[var(--color-border)]'
            }`}
            data-testid={`reaction-${commentId}-${r.emoji}`}
          >
            {r.emoji} {r.count}
          </button>
        )
      })}
      <ReactionPicker onPick={(e) => toggle.mutate(e)} />
    </div>
  )
}

function ReactionPicker({ onPick }: { onPick: (emoji: string) => void }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="relative">
      <button
        onClick={() => setOpen((v) => !v)}
        className="rounded-full border border-[var(--color-border)] px-1.5 py-0.5 text-xs"
        aria-label="Add reaction"
      >
        <Smile className="h-3 w-3" />
      </button>
      {open && (
        <div className="absolute z-10 mt-1 flex gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-1 shadow">
          {QUICK_EMOJI.map((e) => (
            <button
              key={e}
              onClick={() => { onPick(e); setOpen(false) }}
              className="rounded p-1 hover:bg-slate-100 dark:hover:bg-slate-800"
            >
              {e}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

// listReactionsViaListEndpoint is a re-export so the bundle doesn't
// pull the api module twice via inline imports.
import { listReactions as listReactionsViaListEndpoint } from '@/api/comments'

// ---- new comment form ----------------------------------------------------

function NewCommentForm({ documentId }: { documentId: string }) {
  const qc = useQueryClient()
  const [body, setBody] = useState('')
  const create = useMutation({
    mutationFn: () => createComment(documentId, body),
    onSuccess: () => {
      setBody('')
      qc.invalidateQueries({ queryKey: ['comments', documentId] })
    },
    onError: (e: any) => toast.error(e?.response?.data?.error ?? 'failed'),
  })

  return (
    <CommentInput
      value={body}
      onChange={setBody}
      onSubmit={() => body.trim() && create.mutate()}
      submitting={create.isPending}
      placeholder="Comment, or @mention someone…"
      testid="new-comment"
    />
  )
}

function ReplyForm({ parentId, onSubmitted }: { parentId: string; onSubmitted: () => void }) {
  const qc = useQueryClient()
  const [body, setBody] = useState('')
  const reply = useMutation({
    mutationFn: () => replyToComment(parentId, body),
    onSuccess: () => {
      onSubmitted()
      qc.invalidateQueries({ queryKey: ['comments'] })
    },
    onError: (e: any) => toast.error(e?.response?.data?.error ?? 'failed'),
  })
  return (
    <CommentInput
      value={body}
      onChange={setBody}
      onSubmit={() => body.trim() && reply.mutate()}
      submitting={reply.isPending}
      placeholder="Reply…"
      testid={`reply-${parentId}`}
    />
  )
}

// ---- input + @mention autocomplete --------------------------------------

interface CommentInputProps {
  value: string
  onChange: (s: string) => void
  onSubmit: () => void
  submitting: boolean
  placeholder: string
  testid: string
}

function CommentInput({ value, onChange, onSubmit, submitting, placeholder, testid }: CommentInputProps) {
  const taRef = useRef<HTMLTextAreaElement>(null)
  const [mentionQuery, setMentionQuery] = useState<string | null>(null)

  // Detect when the user typed a fresh `@` followed by some chars.
  useEffect(() => {
    const ta = taRef.current
    if (!ta) return
    const pos = ta.selectionStart
    const upTo = value.slice(0, pos)
    const m = upTo.match(/@([A-Za-z0-9._-]*)$/)
    setMentionQuery(m ? m[1] : null)
  }, [value])

  const { data: candidates } = useQuery({
    queryKey: ['mention-search', mentionQuery],
    queryFn: () => getUsers({ search: mentionQuery ?? '' }),
    enabled: mentionQuery !== null,
    staleTime: 30_000,
  })

  const insertMention = (displayName: string, userId: string) => {
    const ta = taRef.current
    if (!ta) return
    const pos = ta.selectionStart
    const upTo = value.slice(0, pos)
    const after = value.slice(pos)
    const replaced = upTo.replace(/@([A-Za-z0-9._-]*)$/, mentionToken(displayName, userId) + ' ')
    onChange(replaced + after)
    setMentionQuery(null)
    setTimeout(() => ta.focus(), 0)
  }

  const items = candidates?.items?.slice(0, 5) ?? []

  return (
    <div className="relative">
      <textarea
        ref={taRef}
        rows={2}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) onSubmit()
        }}
        placeholder={placeholder}
        className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg)] p-2 text-sm"
        data-testid={`${testid}-input`}
      />
      {mentionQuery !== null && items.length > 0 && (
        <ul
          className="absolute bottom-full left-0 z-10 mb-1 max-h-40 w-full overflow-y-auto rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] shadow"
          data-testid={`${testid}-mentions`}
        >
          {items.map((u) => (
            <li key={u.id}>
              <button
                onClick={() => insertMention(u.display_name ?? u.email, u.id)}
                className="block w-full px-2 py-1 text-left text-sm hover:bg-slate-100 dark:hover:bg-slate-800"
              >
                <strong>{u.display_name ?? u.email}</strong>
                <span className="ml-2 text-xs text-[var(--color-text-secondary)]">{u.email}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="mt-1 flex justify-end">
        <Button size="sm" onClick={onSubmit} disabled={!value.trim() || submitting} data-testid={`${testid}-submit`}>
          {submitting ? <Spinner /> : <Send className="h-3 w-3" />} Send
        </Button>
      </div>
    </div>
  )
}

// silence unused: ReactionAggregate is imported for type clarity
export type _ReactionAggregateType = ReactionAggregate
