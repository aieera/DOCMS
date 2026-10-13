import { useTranslation } from 'react-i18next'
import { useState } from 'react'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Plus, Tag as TagIcon, Trash2 } from 'lucide-react'

import { createTag, deleteTag, listTags, type Tag } from '@/api/tags'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/shadcn/input'
import { Skeleton } from '@/components/ui/Skeleton'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { cn } from '@/lib/cn'

const PRESET_COLORS = [
  '#ef4444', '#f97316', '#f59e0b', '#eab308', '#10b981', '#14b8a6',
  '#3b82f6', '#6366f1', '#8b5cf6', '#ec4899', '#64748b', '#888888',
]

export function TagsPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({ queryKey: ['admin', 'tags'], queryFn: listTags })

  const [name, setName] = useState('')
  const [color, setColor] = useState('#3b82f6')
  const [pendingDelete, setPendingDelete] = useState<Tag | null>(null)

  const create = useAppMutation({
    mutationFn: () => createTag({ name: name.trim(), color }),
    onSuccess: () => {
      toast.success(t('adm.tag.created'))
      setName('')
      qc.invalidateQueries({ queryKey: ['admin', 'tags'] })
    },
    onError: (err: unknown) => {
      const m = typeof err === 'object' && err && 'message' in err
        ? String((err as { message?: string }).message)
        : t('adm.tag.err_create')
      toast.error(m)
    },
  })

  const remove = useAppMutation({
    mutationFn: (id: string) => deleteTag(id),
    onSuccess: () => {
      toast.success(t('adm.tag.deleted'))
      setPendingDelete(null)
      qc.invalidateQueries({ queryKey: ['admin', 'tags'] })
    },
    onError: () => toast.error(t('adm.tag.err_delete')),
  })

  return (
    <div className="space-y-6">
      <PageHeader variant="section"
        title={t('adm.tag.title')}
        description={t('adm.tag.description')}
      />

      <Card className="space-y-4 p-5">
        <h3 className="flex items-center gap-2 text-sm font-semibold">
          <Plus className="h-4 w-4" /> {t('adm.tag.new')}
        </h3>
        <div className="flex flex-wrap items-end gap-3">
          <div className="min-w-[200px] flex-1">
            <Input
              label={t('adm.tag.name')}
              placeholder={t('adm.tag.name_ph')}
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={64}
            />
          </div>
          <div>
            <label className="mb-1.5 block text-sm font-medium">{t('adm.tag.color')}</label>
            <div className="flex flex-wrap gap-1.5">
              {PRESET_COLORS.map((c) => (
                <button
                  key={c}
                  type="button"
                  onClick={() => setColor(c)}
                  aria-label={t('adm.tag.pick_color', { hex: c })}
                  className={cn(
                    'h-7 w-7 rounded-full transition-all',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-card',
                    color === c ? 'ring-2 ring-foreground ring-offset-2 ring-offset-card' : 'hover:scale-110',
                  )}
                  style={{ backgroundColor: c }}
                />
              ))}
            </div>
          </div>
          <Button
            onClick={() => {
              if (!name.trim()) { toast.error(t('adm.tag.err_name')); return }
              create.mutate()
            }}
            disabled={create.isPending}
            loading={create.isPending}
          >
            {t('adm.tag.create')}
          </Button>
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          {t('adm.tag.preview')}
          {name.trim() ? (
            <span
              className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium text-white"
              style={{ backgroundColor: color }}
            >
              <TagIcon className="h-3 w-3" />
              {name.trim()}
            </span>
          ) : (
            <span className="italic">{t('adm.tag.preview_empty')}</span>
          )}
        </div>
      </Card>

      {isLoading ? (
        <Skeleton className="h-24" />
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<TagIcon className="h-6 w-6" />}
          title={t('adm.tag.empty')}
          description={t('adm.tag.empty_desc')}
        />
      ) : (
        <Card className="overflow-hidden p-0">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="border-b border-border bg-muted/40">
                <tr>
                  <th className="px-4 py-2.5 text-start text-xs font-semibold uppercase tracking-widest text-muted-foreground">{t('adm.tag.col_tag')}</th>
                  <th className="px-4 py-2.5 text-start text-xs font-semibold uppercase tracking-widest text-muted-foreground">{t('adm.tag.col_documents')}</th>
                  <th className="px-4 py-2.5 text-start"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((tag) => (
                  <tr key={tag.id} className="border-t border-border">
                    <td className="px-4 py-3">
                      <span
                        className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium text-white"
                        style={{ backgroundColor: tag.color }}
                      >
                        <TagIcon className="h-3 w-3" />
                        {tag.name}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-muted-foreground">{tag.document_count.toLocaleString()}</td>
                    <td className="px-4 py-3 text-end">
                      <Button variant="ghost" size="sm" onClick={() => setPendingDelete(tag)} aria-label={t('adm.tag.delete')}>
                        <Trash2 className="h-4 w-4 text-destructive" />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      <ConfirmDialog
        open={!!pendingDelete}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title={pendingDelete ? t('adm.tag.del_title', { name: pendingDelete.name }) : t('adm.tag.del_fallback')}
        description={
          pendingDelete
            ? pendingDelete.document_count > 0
              ? t('adm.tag.del_used', { count: pendingDelete.document_count })
              : t('adm.tag.del_unused')
            : ''
        }
        confirmLabel={t('adm.tag.del_confirm')}
        destructive
        loading={remove.isPending}
        onConfirm={() => pendingDelete && remove.mutate(pendingDelete.id)}
      />
    </div>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/tagging?tab=catalog). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/tags')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/tagging', search: { tab: 'catalog' }, replace: true })
  },
})
