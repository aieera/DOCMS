// Task comment thread with @mention autocomplete (2026-07-28
// task-service design). Flat list, oldest first — no threading, matching
// the task_comments schema.
//
// The mention token format (@[Display Name](uuid)) and the render
// splitter are shared with document comments via @/api/comments, so both
// surfaces stay byte-compatible with the server's parser.
import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Send, Trash2 } from 'lucide-react'

import {
  addComment,
  deleteComment,
  listComments,
  taskKeys,
  type TaskComment,
} from '@/api/tasks'
import { mentionToken, parseBodyForRender } from '@/api/comments'
import { listUserDirectory } from '@/api/auth'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useAuthStore } from '@/store/authStore'
import { Avatar } from '@/components/ui/shadcn/avatar'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { formatRelativeTime } from '@/lib/formatters'

export function TaskComments({ taskId }: { taskId: string }) {
  const qc = useQueryClient()
  const me = useAuthStore((s) => s.user)
  const isAdmin = me?.role === 'admin' || me?.role === 'owner'
  const [body, setBody] = useState('')

  const { data: comments = [], isLoading } = useQuery({
    queryKey: taskKeys.comments(taskId),
    queryFn: () => listComments(taskId),
  })

  const post = useAppMutation({
    mutationFn: () => addComment(taskId, body.trim()),
    onSuccess: () => {
      setBody('')
      void qc.invalidateQueries({ queryKey: taskKeys.comments(taskId) })
      void qc.invalidateQueries({ queryKey: taskKeys.activity(taskId) })
    },
    defaultErrorMessage: 'Could not post comment',
  })

  const remove = useAppMutation({
    mutationFn: (commentId: string) => deleteComment(taskId, commentId),
    onSuccess: () => void qc.invalidateQueries({ queryKey: taskKeys.comments(taskId) }),
    defaultErrorMessage: 'Could not delete comment',
  })

  return (
    <section className="space-y-2" data-testid="task-comments">
      <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        Comments
      </h3>

      {isLoading ? (
        <Spinner />
      ) : comments.length === 0 ? (
        <p className="text-xs text-muted-foreground">No comments yet.</p>
      ) : (
        <ul className="space-y-2">
          {comments.map((c) => (
            <CommentRow
              key={c.id}
              comment={c}
              canDelete={c.author_id === me?.id || isAdmin}
              onDelete={() => remove.mutate(c.id)}
            />
          ))}
        </ul>
      )}

      <CommentInput
        value={body}
        onChange={setBody}
        onSubmit={() => body.trim() && post.mutate(undefined)}
        submitting={post.isPending}
      />
    </section>
  )
}

function CommentRow({
  comment,
  canDelete,
  onDelete,
}: {
  comment: TaskComment
  canDelete: boolean
  onDelete: () => void
}) {
  return (
    <li className="rounded-md border border-border/60 p-2" data-testid={`task-comment-${comment.id}`}>
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <Avatar name={comment.author_id} size="sm" />
        <span>{formatRelativeTime(comment.created_at)}</span>
        {canDelete && (
          <button
            type="button"
            onClick={onDelete}
            aria-label="Delete comment"
            className="ms-auto rounded p-0.5 hover:bg-muted"
          >
            <Trash2 className="h-3 w-3" />
          </button>
        )}
      </div>
      <p className="mt-1 whitespace-pre-wrap text-sm">
        {parseBodyForRender(comment.body).map((seg, i) =>
          seg.userId ? (
            <strong key={i} className="text-primary">
              {seg.text}
            </strong>
          ) : (
            <span key={i}>{seg.text}</span>
          ),
        )}
      </p>
    </li>
  )
}

function CommentInput({
  value,
  onChange,
  onSubmit,
  submitting,
}: {
  value: string
  onChange: (s: string) => void
  onSubmit: () => void
  submitting: boolean
}) {
  const taRef = useRef<HTMLTextAreaElement>(null)
  const [mentionQuery, setMentionQuery] = useState<string | null>(null)

  // A fresh `@` followed by name chars opens the picker; anything else
  // closes it. Same detection the document comment box uses.
  useEffect(() => {
    const ta = taRef.current
    if (!ta) return
    const upTo = value.slice(0, ta.selectionStart)
    const m = upTo.match(/@([A-Za-z0-9._-]*)$/)
    setMentionQuery(m ? m[1] : null)
  }, [value])

  const { data: candidates = [] } = useQuery({
    queryKey: ['mention-search', mentionQuery],
    queryFn: () => listUserDirectory(mentionQuery ?? ''),
    enabled: mentionQuery !== null,
    staleTime: 30_000,
  })

  const insertMention = (displayName: string, userId: string) => {
    const ta = taRef.current
    if (!ta) return
    const pos = ta.selectionStart
    const upTo = value.slice(0, pos)
    const after = value.slice(pos)
    onChange(upTo.replace(/@([A-Za-z0-9._-]*)$/, mentionToken(displayName, userId) + ' ') + after)
    setMentionQuery(null)
    setTimeout(() => ta.focus(), 0)
  }

  const items = candidates.slice(0, 5)

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
        placeholder="Add a comment — @ to mention"
        aria-label="Add a comment"
        className="w-full rounded border border-border bg-background p-2 text-sm"
        data-testid="task-comment-input"
      />
      {mentionQuery !== null && items.length > 0 && (
        <ul
          className="absolute bottom-full start-0 z-10 mb-1 max-h-40 w-full overflow-y-auto rounded-md border border-border bg-card shadow"
          data-testid="task-comment-mentions"
        >
          {items.map((u) => (
            <li key={u.id}>
              <button
                type="button"
                onClick={() => insertMention(u.display_name || u.email, u.id)}
                className="block w-full px-2 py-1 text-start text-sm hover:bg-slate-100 dark:hover:bg-slate-800"
              >
                <strong>{u.display_name || u.email}</strong>
                <span className="ms-2 text-xs text-muted-foreground">{u.email}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="mt-1 flex justify-end">
        <Button size="sm" onClick={onSubmit} disabled={submitting || value.trim() === ''}>
          <Send className="me-1 h-3.5 w-3.5" /> Comment
        </Button>
      </div>
    </div>
  )
}
