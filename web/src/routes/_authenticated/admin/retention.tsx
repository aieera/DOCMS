import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Clock, Plus, Trash2, Power, PowerOff, X } from 'lucide-react'

import {
  createRetentionPolicy,
  deleteRetentionPolicy,
  listRetentionPolicies,
  updateRetentionPolicy,
  type CreatePolicyInput,
  type RetentionPolicy,
} from '@/api/retention'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Skeleton } from '@/components/ui/Skeleton'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { formatRelativeTime } from '@/lib/formatters'

const EMPTY: CreatePolicyInput = {
  name: '',
  description: '',
  document_class_filter: '',
  tag_filter: [],
  retain_days: 365,
  then_action: 'archive',
  archive_days: 30,
  is_active: true,
}

function RetentionPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'retention-policies'],
    queryFn: listRetentionPolicies,
  })

  const [form, setForm] = useState<CreatePolicyInput>(EMPTY)
  const [showForm, setShowForm] = useState(false)
  const [tagInput, setTagInput] = useState('')
  const [pendingDelete, setPendingDelete] = useState<RetentionPolicy | null>(null)

  const create = useMutation({
    mutationFn: () => {
      const payload: CreatePolicyInput = {
        ...form,
        description: form.description || undefined,
        document_class_filter: form.document_class_filter || undefined,
        tag_filter: form.tag_filter && form.tag_filter.length > 0 ? form.tag_filter : undefined,
        archive_days: form.then_action === 'archive' ? form.archive_days || undefined : undefined,
      }
      return createRetentionPolicy(payload)
    },
    onSuccess: () => {
      toast.success('Policy created')
      setForm(EMPTY); setTagInput(''); setShowForm(false)
      qc.invalidateQueries({ queryKey: ['admin', 'retention-policies'] })
    },
    onError: (err: unknown) => {
      const m = typeof err === 'object' && err && 'message' in err
        ? String((err as { message?: string }).message)
        : 'Create failed'
      toast.error(m)
    },
  })

  const toggleActive = useMutation({
    mutationFn: (p: RetentionPolicy) => updateRetentionPolicy(p.id, { is_active: !p.is_active }),
    onSuccess: () => {
      toast.success('Policy updated')
      qc.invalidateQueries({ queryKey: ['admin', 'retention-policies'] })
    },
  })

  const remove = useMutation({
    mutationFn: (id: string) => deleteRetentionPolicy(id),
    onSuccess: () => {
      toast.success('Policy deleted')
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
        title="Retention policies"
        description="Automate document lifecycle. Matching documents get retention_until set at upload time; the daily retention cron archives them or flags them for disposition."
        actions={
          <Button onClick={() => setShowForm((s) => !s)}>
            <Plus className="h-4 w-4" />
            {showForm ? 'Cancel' : 'New policy'}
          </Button>
        }
      />

      {showForm && (
        <Card className="space-y-4 p-5">
          <h3 className="text-sm font-semibold">Create policy</h3>
          <div className="grid gap-4 sm:grid-cols-2">
            <Input label="Name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
            <Input label="Description" value={form.description ?? ''} onChange={(e) => setForm({ ...form, description: e.target.value })} />
            <Input label="Document class (optional)" placeholder="e.g. invoice, contract" value={form.document_class_filter ?? ''} onChange={(e) => setForm({ ...form, document_class_filter: e.target.value })} />
            <Input label="Workspace ID (optional)" placeholder="uuid" value={form.workspace_filter ?? ''} onChange={(e) => setForm({ ...form, workspace_filter: e.target.value })} />
            <Input label="Retain for (days)" type="number" min={1} value={form.retain_days} onChange={(e) => setForm({ ...form, retain_days: parseInt(e.target.value) || 0 })} />
            <Select
              label="Then"
              value={form.then_action}
              onValueChange={(v) => setForm({ ...form, then_action: v as 'archive' | 'dispose' })}
              options={[
                { value: 'archive', label: 'Archive' },
                { value: 'dispose', label: 'Mark for disposition' },
              ]}
            />
            {form.then_action === 'archive' && (
              <Input
                label="Flag for disposition after (archive days)"
                type="number"
                min={0}
                value={form.archive_days ?? 0}
                onChange={(e) => setForm({ ...form, archive_days: parseInt(e.target.value) || 0 })}
              />
            )}
          </div>

          <div>
            <label className="mb-1.5 block text-sm font-medium">Tag filter (any-match)</label>
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
                placeholder={(form.tag_filter ?? []).length === 0 ? 'type tag + Enter' : 'add tag…'}
                value={tagInput}
                onChange={(e) => setTagInput(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addTag() } }}
              />
            </div>
          </div>

          <div className="flex justify-end gap-2 border-t border-border pt-4">
            <Button variant="ghost" onClick={() => { setShowForm(false); setForm(EMPTY); setTagInput('') }} disabled={create.isPending}>
              Cancel
            </Button>
            <Button onClick={() => create.mutate()} disabled={!form.name || form.retain_days <= 0} loading={create.isPending}>
              Create policy
            </Button>
          </div>
        </Card>
      )}

      {isLoading ? (
        <Skeleton className="h-32" />
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Clock className="h-6 w-6" />}
          title="No retention policies"
          description="Create a policy to start applying lifecycle rules. Matching documents get retention_until set automatically once the rules engine sees them."
          actionLabel="Create your first policy"
          onAction={() => setShowForm(true)}
        />
      ) : (
        <Card className="overflow-hidden p-0">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-muted/40">
                <tr className="text-left">
                  <th className="px-4 py-2.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">Policy</th>
                  <th className="px-4 py-2.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">Scope</th>
                  <th className="px-4 py-2.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">Retain</th>
                  <th className="px-4 py-2.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">Then</th>
                  <th className="px-4 py-2.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">Status</th>
                  <th className="px-4 py-2.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">Updated</th>
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
                    <td className="px-4 py-3 font-mono text-xs">{p.retain_days}d</td>
                    <td className="px-4 py-3 text-xs">
                      <span className="capitalize">{p.then_action}</span>
                      {p.then_action === 'archive' && p.archive_days ? <span className="text-muted-foreground"> → flag after {p.archive_days}d</span> : ''}
                    </td>
                    <td className="px-4 py-3">
                      <Badge variant={p.is_active ? 'active' : 'archived'}>{p.is_active ? 'Active' : 'Paused'}</Badge>
                    </td>
                    <td className="px-4 py-3 text-xs text-muted-foreground">{formatRelativeTime(p.updated_at)}</td>
                    <td className="px-4 py-3 text-right">
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" size="sm" onClick={() => toggleActive.mutate(p)} disabled={toggleActive.isPending} aria-label={p.is_active ? 'Pause' : 'Activate'}>
                          {p.is_active ? <PowerOff className="h-4 w-4" /> : <Power className="h-4 w-4" />}
                        </Button>
                        <Button variant="ghost" size="sm" onClick={() => setPendingDelete(p)} aria-label="Delete">
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
        title={pendingDelete ? `Delete "${pendingDelete.name}"?` : 'Delete policy'}
        description="The policy is removed immediately. Documents already affected keep their existing retention_until — only future matches are unscoped."
        confirmLabel="Delete policy"
        destructive
        loading={remove.isPending}
        onConfirm={() => pendingDelete && remove.mutate(pendingDelete.id)}
      />
    </div>
  )
}

function scopeSummary(p: RetentionPolicy): string {
  const bits: string[] = []
  if (p.document_class_filter) bits.push(`class=${p.document_class_filter}`)
  if (p.workspace_filter) bits.push(`ws=${p.workspace_filter.slice(0, 8)}…`)
  if (p.tag_filter && p.tag_filter.length > 0) bits.push(`tags=[${p.tag_filter.join(',')}]`)
  return bits.length > 0 ? bits.join(' · ') : 'all documents'
}

export const Route = createFileRoute('/_authenticated/admin/retention')({ component: RetentionPage })
