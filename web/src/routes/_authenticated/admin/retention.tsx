import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Clock, Plus, Trash2, Power, PowerOff } from 'lucide-react'

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
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'
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
      setForm(EMPTY)
      setTagInput('')
      setShowForm(false)
      qc.invalidateQueries({ queryKey: ['admin', 'retention-policies'] })
    },
    onError: (err: unknown) => {
      const m =
        typeof err === 'object' && err && 'message' in err
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
    },
  })

  const addTag = () => {
    const t = tagInput.trim()
    if (!t) return
    setForm((f) => ({ ...f, tag_filter: [...(f.tag_filter ?? []), t] }))
    setTagInput('')
  }

  return (
    <div>
      <PageHeader
        title="Retention Policies"
        description="Automate document lifecycle. Matching docs get their retention_until set at upload time; the daily retention cron archives or flags them for disposition."
        actions={
          <Button onClick={() => setShowForm((s) => !s)}>
            <Plus className="h-4 w-4" />
            {showForm ? 'Cancel' : 'New policy'}
          </Button>
        }
      />

      {showForm && (
        <div className="mb-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <Field label="Name">
              <input
                className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            </Field>
            <Field label="Description">
              <input
                className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
                value={form.description}
                onChange={(e) => setForm({ ...form, description: e.target.value })}
              />
            </Field>
            <Field label="Document class (optional)">
              <input
                className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
                placeholder="e.g. invoice, contract"
                value={form.document_class_filter}
                onChange={(e) => setForm({ ...form, document_class_filter: e.target.value })}
              />
            </Field>
            <Field label="Workspace ID (optional)">
              <input
                className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
                placeholder="uuid"
                value={form.workspace_filter ?? ''}
                onChange={(e) => setForm({ ...form, workspace_filter: e.target.value })}
              />
            </Field>
            <Field label="Retain for (days)">
              <input
                type="number"
                min={1}
                className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
                value={form.retain_days}
                onChange={(e) => setForm({ ...form, retain_days: parseInt(e.target.value) || 0 })}
              />
            </Field>
            <Field label="Then">
              <select
                className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
                value={form.then_action}
                onChange={(e) => setForm({ ...form, then_action: e.target.value as 'archive' | 'dispose' })}
              >
                <option value="archive">Archive</option>
                <option value="dispose">Mark for disposition</option>
              </select>
            </Field>
            {form.then_action === 'archive' && (
              <Field label="Flag for disposition after (archive days)">
                <input
                  type="number"
                  min={0}
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
                  value={form.archive_days}
                  onChange={(e) => setForm({ ...form, archive_days: parseInt(e.target.value) || 0 })}
                />
              </Field>
            )}
          </div>

          <Field label="Tag filter (optional, any-match)">
            <div className="flex flex-wrap gap-1">
              {(form.tag_filter ?? []).map((t, i) => (
                <span key={i} className="inline-flex items-center gap-1 rounded-full bg-[var(--color-accent)] px-2 py-0.5 text-xs">
                  {t}
                  <button
                    onClick={() =>
                      setForm((f) => ({
                        ...f,
                        tag_filter: (f.tag_filter ?? []).filter((_, j) => j !== i),
                      }))
                    }
                  >
                    ×
                  </button>
                </span>
              ))}
              <input
                className="flex-1 min-w-[120px] rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
                placeholder="type tag + Enter"
                value={tagInput}
                onChange={(e) => setTagInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    addTag()
                  }
                }}
              />
            </div>
          </Field>

          <div className="flex gap-2">
            <Button
              onClick={() => create.mutate()}
              disabled={!form.name || form.retain_days <= 0 || create.isPending}
            >
              Create policy
            </Button>
          </div>
        </div>
      )}

      {isLoading ? (
        <Skeleton className="h-24" />
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Clock className="h-12 w-12" />}
          title="No retention policies"
          description="Create a policy above. Matching documents get their retention_until set automatically once the rules engine is enabled."
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-[var(--color-border)]">
          <table className="w-full text-sm">
            <thead className="bg-slate-50 dark:bg-slate-800/50">
              <tr className="text-left">
                <th className="px-4 py-2">Policy</th>
                <th className="px-4 py-2">Scope</th>
                <th className="px-4 py-2">Retain</th>
                <th className="px-4 py-2">Then</th>
                <th className="px-4 py-2">Status</th>
                <th className="px-4 py-2">Updated</th>
                <th className="px-4 py-2"></th>
              </tr>
            </thead>
            <tbody>
              {data.map((p) => (
                <tr key={p.id} className="border-t border-[var(--color-border)]">
                  <td className="px-4 py-2">
                    <div className="font-medium">{p.name}</div>
                    {p.description && (
                      <div className="text-xs text-[var(--color-text-secondary)]">{p.description}</div>
                    )}
                  </td>
                  <td className="px-4 py-2 text-xs">
                    {scopeSummary(p)}
                  </td>
                  <td className="px-4 py-2">{p.retain_days}d</td>
                  <td className="px-4 py-2">
                    {p.then_action}
                    {p.then_action === 'archive' && p.archive_days
                      ? ` → flag after ${p.archive_days}d`
                      : ''}
                  </td>
                  <td className="px-4 py-2">
                    <Badge variant={p.is_active ? 'active' : 'archived'}>
                      {p.is_active ? 'Active' : 'Paused'}
                    </Badge>
                  </td>
                  <td className="px-4 py-2 text-xs text-[var(--color-text-secondary)]">
                    {formatRelativeTime(p.updated_at)}
                  </td>
                  <td className="px-4 py-2 text-right">
                    <div className="flex justify-end gap-1">
                      <Button onClick={() => toggleActive.mutate(p)} disabled={toggleActive.isPending}>
                        {p.is_active ? <PowerOff className="h-4 w-4" /> : <Power className="h-4 w-4" />}
                      </Button>
                      <Button
                        onClick={() => {
                          if (window.confirm(`Delete policy "${p.name}"?`)) remove.mutate(p.id)
                        }}
                        disabled={remove.isPending}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
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

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block text-xs">
      <span className="mb-1 block text-[var(--color-text-secondary)]">{label}</span>
      {children}
    </label>
  )
}

function scopeSummary(p: RetentionPolicy): string {
  const bits: string[] = []
  if (p.document_class_filter) bits.push(`class=${p.document_class_filter}`)
  if (p.workspace_filter) bits.push(`ws=${p.workspace_filter.slice(0, 8)}`)
  if (p.tag_filter && p.tag_filter.length > 0) bits.push(`tags=[${p.tag_filter.join(',')}]`)
  return bits.length > 0 ? bits.join(' · ') : 'all documents'
}

export const Route = createFileRoute('/_authenticated/admin/retention')({ component: RetentionPage })
