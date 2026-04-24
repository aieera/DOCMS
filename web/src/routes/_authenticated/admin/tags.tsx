import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Tag as TagIcon, Plus, Trash2 } from 'lucide-react'

import { createTag, deleteTag, listTags, type Tag } from '@/api/tags'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'

const PRESET_COLORS = [
  '#ef4444', '#f97316', '#f59e0b', '#eab308', '#10b981', '#14b8a6',
  '#3b82f6', '#6366f1', '#8b5cf6', '#ec4899', '#64748b', '#888888',
]

function TagsPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({ queryKey: ['admin', 'tags'], queryFn: listTags })

  const [name, setName] = useState('')
  const [color, setColor] = useState('#3b82f6')

  const create = useMutation({
    mutationFn: () => createTag({ name: name.trim(), color }),
    onSuccess: () => {
      toast.success('Tag created')
      setName('')
      qc.invalidateQueries({ queryKey: ['admin', 'tags'] })
    },
    onError: (err: unknown) => {
      const m =
        typeof err === 'object' && err && 'message' in err
          ? String((err as { message?: string }).message)
          : 'Create failed'
      toast.error(m)
    },
  })

  const remove = useMutation({
    mutationFn: (id: string) => deleteTag(id),
    onSuccess: () => {
      toast.success('Tag deleted')
      qc.invalidateQueries({ queryKey: ['admin', 'tags'] })
    },
    onError: () => toast.error('Delete failed'),
  })

  const confirmDelete = (t: Tag) => {
    const count = t.document_count ?? 0
    const suffix =
      count > 0
        ? ` It is applied to ${count} document${count === 1 ? '' : 's'}; the tag will be removed from them too.`
        : ''
    if (window.confirm(`Delete tag "${t.name}"?${suffix}`)) remove.mutate(t.id)
  }

  return (
    <div>
      <PageHeader
        title="Tags"
        description="Tenant-level tag catalog. Per-document tagging lives on each document."
      />

      <div className="mb-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
        <h3 className="mb-3 flex items-center gap-2 font-medium">
          <Plus className="h-4 w-4" /> New tag
        </h3>
        <div className="flex flex-wrap items-center gap-3">
          <input
            className="flex-1 min-w-[180px] rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
            placeholder="Tag name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={64}
          />
          <div className="flex flex-wrap gap-1">
            {PRESET_COLORS.map((c) => (
              <button
                key={c}
                onClick={() => setColor(c)}
                aria-label={c}
                className={`h-6 w-6 rounded-full border-2 transition ${
                  color === c ? 'border-[var(--color-primary)]' : 'border-transparent'
                }`}
                style={{ backgroundColor: c }}
              />
            ))}
          </div>
          <Button onClick={() => create.mutate()} disabled={!name.trim() || create.isPending}>
            Create
          </Button>
        </div>
        <div className="mt-2 flex items-center gap-2 text-xs text-[var(--color-text-secondary)]">
          Preview:
          <span
            className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium text-white"
            style={{ backgroundColor: color }}
          >
            <TagIcon className="h-3 w-3" />
            {name || 'tag-name'}
          </span>
        </div>
      </div>

      {isLoading ? (
        <Skeleton className="h-24" />
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<TagIcon className="h-12 w-12" />}
          title="No tags yet"
          description="Create tenant-wide tags above, then apply them to documents."
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-[var(--color-border)]">
          <table className="w-full text-sm">
            <thead className="bg-slate-50 dark:bg-slate-800/50">
              <tr className="text-left">
                <th className="px-4 py-2">Tag</th>
                <th className="px-4 py-2">Documents</th>
                <th className="px-4 py-2"></th>
              </tr>
            </thead>
            <tbody>
              {data.map((t) => (
                <tr key={t.id} className="border-t border-[var(--color-border)]">
                  <td className="px-4 py-2">
                    <span
                      className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium text-white"
                      style={{ backgroundColor: t.color }}
                    >
                      <TagIcon className="h-3 w-3" />
                      {t.name}
                    </span>
                  </td>
                  <td className="px-4 py-2">{(t.document_count ?? 0).toLocaleString()}</td>
                  <td className="px-4 py-2 text-right">
                    <Button onClick={() => confirmDelete(t)} disabled={remove.isPending}>
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/tags')({ component: TagsPage })
