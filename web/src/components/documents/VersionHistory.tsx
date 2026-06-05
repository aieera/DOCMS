import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Download, RotateCcw, AlertTriangle, Pencil, Check, X } from 'lucide-react'

import { getVersions, setVersionLabel } from '@/api/documents'
import { readErrorMessage } from '@/api/client'
import { Avatar } from '@/components/ui/shadcn/avatar'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatFileSize, formatRelativeTime } from '@/lib/formatters'
import { Button } from '@/components/ui/shadcn/button'
import { Badge } from '@/components/ui/shadcn/badge'
import type { Version } from '@/types/api'

export function VersionHistory({ documentId }: { documentId: string }) {
  // Wave 5 pattern 3: getVersions throws UnknownListShapeError on
  // malformed responses (was silently returning []), so this consumer
  // renders an isError branch — without it the throw routes here as
  // `data === undefined` and the surface looks identical to "doc with
  // no versions", which was the silent-fail bug we fixed at the source.
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ['versions', documentId],
    queryFn: () => getVersions(documentId),
  })

  if (isLoading) return <div className="space-y-2">{[1, 2, 3].map((i) => <Skeleton key={i} className="h-14" />)}</div>

  if (isError) {
    return (
      <div className="flex items-start gap-3 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" aria-hidden />
        <div className="flex-1">
          <p className="font-medium text-destructive">Could not load version history.</p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            The server returned an unexpected response. Try again or refresh the page.
          </p>
          <Button
            variant="outline"
            size="sm"
            className="mt-2"
            onClick={() => void refetch()}
            data-testid="versions-retry"
          >
            Retry
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-2">
      {data?.map((v) => (
        <VersionRow key={v.id} version={v} documentId={documentId} />
      ))}
    </div>
  )
}

// Single version row — splits out so the inline-edit state for the
// label is per-row instead of one giant Map keyed by version id.
function VersionRow({ version, documentId }: { version: Version; documentId: string }) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(version.label ?? '')

  const rename = useAppMutation({
    mutationFn: (label: string) => setVersionLabel(documentId, version.id, label),
    onSuccess: () => {
      toast.success(draft.trim() ? 'Version renamed' : 'Version label cleared')
      qc.invalidateQueries({ queryKey: ['versions', documentId] })
      setEditing(false)
    },
    onError: (e: unknown) => {
      toast.error(readErrorMessage(e) ?? 'Could not update version label')
    },
  })

  const startEdit = () => {
    setDraft(version.label ?? '')
    setEditing(true)
  }
  const cancelEdit = () => {
    setDraft(version.label ?? '')
    setEditing(false)
  }
  const submit = () => {
    const trimmed = draft.trim()
    // 200-char ceiling mirrors backend (service.SetVersionLabel).
    if (trimmed.length > 200) {
      toast.error('Label must be 200 characters or fewer')
      return
    }
    rename.mutate(trimmed)
  }

  return (
    <div
      className="flex items-start gap-3 rounded-md border border-[var(--color-border)] p-3"
      data-testid={`version-row-${version.id}`}
    >
      <Avatar name={version.created_by_name} size="sm" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2">
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <span className="text-sm font-medium">v{version.version_number}</span>
            {editing ? (
              <input
                type="text"
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') submit()
                  if (e.key === 'Escape') cancelEdit()
                }}
                placeholder="Name this version (optional)"
                maxLength={200}
                autoFocus
                className="min-w-0 flex-1 rounded border border-border bg-background px-1.5 py-0.5 text-xs"
                data-testid={`version-label-input-${version.id}`}
              />
            ) : version.label ? (
              <Badge variant="default" data-testid={`version-label-${version.id}`}>
                {version.label}
              </Badge>
            ) : (
              <button
                type="button"
                onClick={startEdit}
                className="text-xs text-muted-foreground hover:text-foreground hover:underline"
                data-testid={`version-name-cta-${version.id}`}
              >
                Name this version
              </button>
            )}
          </div>
          <span className="shrink-0 text-xs text-[var(--color-text-secondary)]">{formatRelativeTime(version.created_at)}</span>
        </div>
        {version.change_summary && <p className="mt-0.5 text-xs text-[var(--color-text-secondary)]">{version.change_summary}</p>}
        <div className="mt-1 flex items-center gap-2">
          <span className="text-xs text-[var(--color-text-secondary)]">{formatFileSize(version.size_bytes)}</span>
          {editing ? (
            <>
              <Button
                variant="ghost"
                size="sm"
                className="h-6 px-1.5"
                onClick={submit}
                loading={rename.isPending}
                disabled={rename.isPending}
                aria-label="Save version label"
                data-testid={`version-label-save-${version.id}`}
              >
                <Check className="h-3 w-3" />
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="h-6 px-1.5"
                onClick={cancelEdit}
                disabled={rename.isPending}
                aria-label="Cancel rename"
                data-testid={`version-label-cancel-${version.id}`}
              >
                <X className="h-3 w-3" />
              </Button>
            </>
          ) : (
            <>
              <Button variant="ghost" size="sm" className="h-6 px-1.5"><Download className="h-3 w-3" /></Button>
              <Button variant="ghost" size="sm" className="h-6 px-1.5"><RotateCcw className="h-3 w-3" /></Button>
              {version.label && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 px-1.5"
                  onClick={startEdit}
                  aria-label="Edit version label"
                  data-testid={`version-label-edit-${version.id}`}
                >
                  <Pencil className="h-3 w-3" />
                </Button>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}
