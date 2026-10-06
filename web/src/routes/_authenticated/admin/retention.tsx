import { useMemo, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import i18n from '@/i18n'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Clock, Plus, Trash2, Power, PowerOff, X, Eye } from 'lucide-react'

import {
  createRetentionPolicy,
  deleteRetentionPolicy,
  listRetentionPolicies,
  updateRetentionPolicy,
  previewRetentionPolicy,
  type CreatePolicyInput,
  type RetentionPolicy,
  type RetentionPreviewResult,
} from '@/api/retention'
import { getWorkspaces, getFolders } from '@/api/workspaces'
import { readErrorMessage } from '@/api/client'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { lifecycleVariant } from '@/lib/lifecycle'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Skeleton } from '@/components/ui/Skeleton'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { Dialog } from '@/components/ui/Dialog'
import { formatRelativeTime } from '@/lib/formatters'

const EMPTY: CreatePolicyInput = {
  name: '',
  description: '',
  document_class_filter: '',
  tag_filter: [],
  workspace_filter: '',
  folder_filter: '',
  retain_days: 365,
  then_action: 'archive',
  archive_days: 30,
  is_active: true,
}

export function RetentionPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'retention-policies'],
    queryFn: listRetentionPolicies,
  })

  const [form, setForm] = useState<CreatePolicyInput>(EMPTY)
  const [showForm, setShowForm] = useState(false)
  const [tagInput, setTagInput] = useState('')
  const [pendingDelete, setPendingDelete] = useState<RetentionPolicy | null>(null)
  const [previewOpen, setPreviewOpen] = useState(false)
  const [previewResult, setPreviewResult] = useState<RetentionPreviewResult | null>(null)

  // Workspace + folder pickers replace the previous raw-UUID inputs.
  // Folders are loaded lazily once a workspace is picked so a tenant
  // with many empty workspaces doesn't issue N folder queries on form
  // mount.
  const workspacesQ = useQuery({
    queryKey: ['workspaces'],
    queryFn: getWorkspaces,
    staleTime: 60_000,
  })
  const foldersQ = useQuery({
    queryKey: ['folders', form.workspace_filter || '', 'root'],
    queryFn: () => getFolders(form.workspace_filter!),
    enabled: !!form.workspace_filter,
  })
  const folderOptions = useMemo(() => {
    return (foldersQ.data ?? []).map((f) => ({ value: f.id, label: f.name }))
  }, [foldersQ.data])

  // Build the payload from form state, stripping empty strings so the
  // backend sees nullable fields as omitted rather than overwriting to
  // empty. Shared by create + preview so they evaluate the same draft.
  const buildPayload = (): CreatePolicyInput => ({
    ...form,
    description: form.description || undefined,
    document_class_filter: form.document_class_filter || undefined,
    tag_filter: form.tag_filter && form.tag_filter.length > 0 ? form.tag_filter : undefined,
    workspace_filter: form.workspace_filter || undefined,
    folder_filter: form.folder_filter || undefined,
    archive_days: form.then_action === 'archive' ? form.archive_days || undefined : undefined,
  })

  const create = useAppMutation({
    mutationFn: () => createRetentionPolicy(buildPayload()),
    onSuccess: () => {
      toast.success(t('adm.ret.created'))
      setForm(EMPTY); setTagInput(''); setShowForm(false); setPreviewResult(null)
      qc.invalidateQueries({ queryKey: ['admin', 'retention-policies'] })
    },
    onError: (err: unknown) => toast.error(readErrorMessage(err) ?? 'Create failed'),
  })

  const preview = useAppMutation({
    mutationFn: () => previewRetentionPolicy(buildPayload()),
    onSuccess: (res) => {
      setPreviewResult(res)
      setPreviewOpen(true)
    },
    onError: (err: unknown) => toast.error(readErrorMessage(err) ?? 'Preview failed'),
  })

  const toggleActive = useAppMutation({
    mutationFn: (p: RetentionPolicy) => updateRetentionPolicy(p.id, { is_active: !p.is_active }),
    onSuccess: () => {
      toast.success(t('adm.ret.updated'))
      qc.invalidateQueries({ queryKey: ['admin', 'retention-policies'] })
    },
  })

  const remove = useAppMutation({
    mutationFn: (id: string) => deleteRetentionPolicy(id),
    onSuccess: () => {
      toast.success(t('adm.ret.deleted'))
      qc.invalidateQueries({ queryKey: ['admin', 'retention-policies'] })
      setPendingDelete(null)
    },
  })

  const addTag = () => {
    const t = tagInput.trim()
    if (!t) return
    setForm((f) => ({ ...f, tag_filter: [...(f.tag_filter ?? []), t] }))
    setTagInput('')
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('adm.ret.title')}
        description={t('adm.ret.description')}
        actions={
          <Button onClick={() => setShowForm((s) => !s)}>
            <Plus className="h-4 w-4" />
            {showForm ? t('adm.ret.cancel') : t('adm.ret.new')}
          </Button>
        }
      />

      {showForm && (
        <Card className="space-y-4 p-5">
          <h3 className="text-sm font-semibold">{t('adm.ret.create_title')}</h3>
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <Input label={t('adm.ret.name')} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
            <Input label={t('adm.ret.desc')} value={form.description ?? ''} onChange={(e) => setForm({ ...form, description: e.target.value })} />
            <Input label={t('adm.ret.doc_class')} placeholder={t('adm.ret.class_placeholder')} value={form.document_class_filter ?? ''} onChange={(e) => setForm({ ...form, document_class_filter: e.target.value })} />
            <Select
              label={t('adm.ret.workspace')}
              // Radix Select.Item rejects an empty-string value (it throws and
              // the root error boundary blanks the whole app), so the "All"
              // option uses a sentinel mapped back to '' in form state.
              value={form.workspace_filter || '__all__'}
              onValueChange={(v) => setForm({ ...form, workspace_filter: v === '__all__' ? '' : v, folder_filter: '' })}
              placeholder={t('adm.ret.all_workspaces')}
              options={[
                { value: '__all__', label: t('adm.ret.all_workspaces') },
                ...(workspacesQ.data ?? []).map((w) => ({ value: w.id, label: w.name })),
              ]}
            />
            <Select
              label={t('adm.ret.folder')}
              value={form.folder_filter || '__all__'}
              onValueChange={(v) => setForm({ ...form, folder_filter: v === '__all__' ? '' : v })}
              placeholder={form.workspace_filter ? t('adm.ret.folders_in_ws') : t('adm.ret.pick_workspace')}
              disabled={!form.workspace_filter || foldersQ.isLoading}
              options={[
                { value: '__all__', label: t('adm.ret.all_folders') },
                ...folderOptions,
              ]}
            />
            <Input label={t('adm.ret.retain_days')} type="number" min={1} value={form.retain_days} onChange={(e) => setForm({ ...form, retain_days: parseInt(e.target.value) || 0 })} />
            <Select
              label={t('adm.ret.then')}
              value={form.then_action}
              onValueChange={(v) => setForm({ ...form, then_action: v as 'archive' | 'dispose' })}
              options={[
                { value: 'archive', label: t('adm.ret.archive') },
                { value: 'dispose', label: t('adm.ret.dispose') },
              ]}
            />
            {form.then_action === 'archive' && (
              <Input
                label={t('adm.ret.archive_days')}
                type="number"
                min={0}
                value={form.archive_days ?? 0}
                onChange={(e) => setForm({ ...form, archive_days: parseInt(e.target.value) || 0 })}
              />
            )}
          </div>

          <div>
            <label className="mb-1.5 block text-sm font-medium">{t('adm.ret.tag_filter')}</label>
            <div className="flex flex-wrap items-center gap-2 rounded-md border border-input bg-background p-2">
              {(form.tag_filter ?? []).map((t, i) => (
                <span key={i} className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs">
                  {t}
                  <button
                    type="button"
                    aria-label={`Remove ${t}`}
                    onClick={() => setForm((f) => ({ ...f, tag_filter: (f.tag_filter ?? []).filter((_, j) => j !== i) }))}
                    className="text-muted-foreground hover:text-foreground"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </span>
              ))}
              <input
                className="flex-1 min-w-[120px] bg-transparent text-sm placeholder:text-muted-foreground focus:outline-none"
                placeholder={(form.tag_filter ?? []).length === 0 ? t('adm.ret.tag_placeholder') : t('adm.ret.tag_add')}
                value={tagInput}
                onChange={(e) => setTagInput(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addTag() } }}
              />
            </div>
          </div>

          <div className="flex justify-end gap-2 border-t border-border pt-4">
            <Button
              variant="ghost"
              onClick={() => { setShowForm(false); setForm(EMPTY); setTagInput(''); setPreviewResult(null) }}
              disabled={create.isPending || preview.isPending}
            >
              {t('adm.ret.cancel')}
            </Button>
            <Button
              variant="outline"
              onClick={() => {
                if (form.retain_days <= 0) { toast.error(t('adm.ret.err_days')); return }
                preview.mutate()
              }}
              disabled={create.isPending || preview.isPending}
              loading={preview.isPending}
              data-testid="retention-preview"
            >
              <Eye className="h-4 w-4" /> {t('adm.ret.preview')}
            </Button>
            <Button
              onClick={() => {
                if (!form.name?.trim()) { toast.error(t('adm.ret.err_name')); return }
                if (form.retain_days <= 0) { toast.error(t('adm.ret.err_days')); return }
                create.mutate()
              }}
              disabled={create.isPending}
              loading={create.isPending}
            >
              {t('adm.ret.create_title')}
            </Button>
          </div>
        </Card>
      )}

      {isLoading ? (
        <Skeleton className="h-32" />
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Clock className="h-6 w-6" />}
          title={t('adm.ret.empty')}
          description={t('adm.ret.empty_desc')}
          actionLabel={t('adm.ret.empty_action')}
          onAction={() => setShowForm(true)}
        />
      ) : (
        <Card className="overflow-hidden p-0">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-muted/40">
                <tr>
                  <th className="px-4 py-2.5 text-start text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.ret.col_policy')}</th>
                  <th className="px-4 py-2.5 text-start text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.ret.col_scope')}</th>
                  <th className="px-4 py-2.5 text-start text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.ret.col_retain')}</th>
                  <th className="px-4 py-2.5 text-start text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.ret.col_then')}</th>
                  <th className="px-4 py-2.5 text-start text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.ret.col_status')}</th>
                  <th className="px-4 py-2.5 text-start text-xs font-medium uppercase tracking-wider text-muted-foreground">{t('adm.ret.col_updated')}</th>
                  <th className="px-4 py-2.5"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((p) => (
                  <tr key={p.id} className="border-t border-border">
                    <td className="px-4 py-3">
                      <div className="text-sm font-medium">{p.name}</div>
                      {p.description && <div className="text-xs text-muted-foreground">{p.description}</div>}
                    </td>
                    <td className="px-4 py-3 text-xs text-muted-foreground">{scopeSummary(p)}</td>
                    <td dir="auto" className="px-4 py-3 font-mono text-xs">{p.retain_days}d</td>
                    <td className="px-4 py-3 text-xs">
                      <span>{t(`adm.ret.act_${p.then_action}`)}</span>
                      {p.then_action === 'archive' && p.archive_days
                        ? <span className="text-muted-foreground"> {t('adm.ret.flag_after', { days: p.archive_days })}</span>
                        : ''}
                    </td>
                    <td className="px-4 py-3">
                      <Badge variant={p.is_active ? 'active' : 'archived'}>{p.is_active ? t('adm.ret.status_active') : t('adm.ret.status_paused')}</Badge>
                    </td>
                    <td dir="auto" className="px-4 py-3 text-xs text-muted-foreground">{formatRelativeTime(p.updated_at)}</td>
                    <td className="px-4 py-3 text-end">
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" size="sm" onClick={() => toggleActive.mutate(p)} disabled={toggleActive.isPending} aria-label={p.is_active ? t('adm.ret.pause') : t('adm.ret.activate')}>
                          {p.is_active ? <PowerOff className="h-4 w-4" /> : <Power className="h-4 w-4" />}
                        </Button>
                        <Button variant="ghost" size="sm" onClick={() => setPendingDelete(p)} aria-label={t('adm.ret.delete')}>
                          <Trash2 className="h-4 w-4 text-destructive" />
                        </Button>
                      </div>
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
        title={pendingDelete ? t('adm.ret.delete_named', { name: pendingDelete.name }) : t('adm.ret.delete_confirm')}
        description={t('adm.ret.delete_desc')}
        confirmLabel={t('adm.ret.delete_confirm')}
        destructive
        loading={remove.isPending}
        onConfirm={() => pendingDelete && remove.mutate(pendingDelete.id)}
      />

      <Dialog
        open={previewOpen}
        onOpenChange={setPreviewOpen}
        title={t('adm.ret.preview_title')}
        size="xl"
      >
        {previewResult ? (
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground" data-testid="retention-preview-count">
              {/* <Trans> because the bold count sits mid-sentence and Arabic
                  reorders the clauses around it; the `s`-appending ternary
                  this replaces encoded an English-only plural rule. */}
              <Trans
                i18nKey="adm.ret.preview_count"
                ns="common"
                count={previewResult.count}
                values={{ total: previewResult.count.toLocaleString(), sample: previewResult.sample.length }}
                components={[<strong key="n" className="text-foreground" />]}
              />
            </p>
            {previewResult.sample.length === 0 ? (
              <p className="rounded-md border border-dashed border-border bg-muted/30 p-4 text-sm text-muted-foreground">
                {t('adm.ret.no_matches')}
              </p>
            ) : (
              <div className="max-h-96 overflow-x-auto overflow-y-auto rounded-md border border-border">
                <table className="w-full text-sm">
                  <thead className="bg-muted/40">
                    <tr>
                      <th className="px-3 py-2 text-start">{t('adm.ret.pcol_title')}</th>
                      <th className="px-3 py-2 text-start">{t('adm.ret.pcol_class')}</th>
                      <th className="px-3 py-2 text-start">{t('adm.ret.pcol_state')}</th>
                      <th className="px-3 py-2 text-start">{t('adm.ret.pcol_created')}</th>
                    </tr>
                  </thead>
                  <tbody data-testid="retention-preview-sample">
                    {previewResult.sample.map((d) => (
                      <tr key={d.id} className="border-t border-border">
                        <td className="px-3 py-2">{d.title}</td>
                        <td className="px-3 py-2 text-xs">{d.document_class || <span className="text-muted-foreground">—</span>}</td>
                        <td className="px-3 py-2"><Badge variant={lifecycleVariant(d.lifecycle_state)}>{d.lifecycle_state}</Badge></td>
                        <td className="px-3 py-2 text-xs text-muted-foreground">{formatRelativeTime(d.created_at)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <div className="flex justify-end pt-2">
              <Button onClick={() => setPreviewOpen(false)}>{t('adm.ret.close')}</Button>
            </div>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t('adm.ret.no_preview')}</p>
        )}
      </Dialog>
    </div>
  )
}

const tr = (key: string, value: string) =>
  i18n.t(`adm.ret.${key}`, { ns: 'common', value })

function scopeSummary(p: RetentionPolicy): string {
  const bits: string[] = []
  if (p.document_class_filter) bits.push(tr('scope_class', p.document_class_filter))
  if (p.workspace_filter) bits.push(tr('scope_ws', `${p.workspace_filter.slice(0, 8)}…`))
  if (p.folder_filter) bits.push(tr('scope_folder', `${p.folder_filter.slice(0, 8)}…`))
  if (p.tag_filter && p.tag_filter.length > 0) bits.push(tr('scope_tags', p.tag_filter.join(',')))
  return bits.length > 0 ? bits.join(' · ') : i18n.t('adm.ret.all_documents', { ns: 'common', defaultValue: 'all documents' })
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/records-retention?tab=retention). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/retention')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/records-retention', search: { tab: 'retention' }, replace: true })
  },
})
