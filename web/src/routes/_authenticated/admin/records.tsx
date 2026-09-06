import { useMemo, useState } from 'react'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ChevronRight, FolderTree, Trash2, Plus, ShieldCheck, Clock } from 'lucide-react'

import {
  listSchedules, createSchedule, deleteSchedule,
  listCategories, createCategory, updateCategory, deleteCategory,
  dispositionQueue, disposeRecord,
  type RetentionSchedule, type RecordCategory, type RecordRow,
  type TriggerEvent, type DispositionAction, type NodeType,
} from '@/api/records'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useAuthStore } from '@/store/authStore'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { formatDate } from '@/lib/formatters'

const WRITE_ROLES = ['owner', 'admin', 'compliance_officer']
const TRIGGERS: TriggerEvent[] = ['declaration', 'creation', 'event', 'superseded', 'fixed_date']
const ACTIONS: DispositionAction[] = ['destroy', 'transfer', 'permanent', 'review']

type Tab = 'plan' | 'schedules' | 'queue'

export function RecordsAdminPage() {
  const role = useAuthStore((s) => s.user?.role) ?? ''
  const canWrite = WRITE_ROLES.includes(role)
  const [tab, setTab] = useState<Tab>('plan')

  return (
    <div className="mx-auto max-w-5xl p-6">
      <PageHeader
        title="Records management"
        description="File plan · retention schedules · record declaration & disposition."
      />
      <div className="mt-4 flex gap-1 border-b border-border">
        {([['plan', 'File plan'], ['schedules', 'Retention schedules'], ['queue', 'Disposition queue']] as [Tab, string][]).map(
          ([key, label]) => (
            <button
              key={key}
              onClick={() => setTab(key)}
              className={`px-3 py-2 text-sm font-medium ${tab === key ? 'border-b-2 border-primary text-foreground' : 'text-muted-foreground'}`}
              data-testid={`records-tab-${key}`}
            >
              {label}
            </button>
          ),
        )}
      </div>

      <div className="mt-4">
        {tab === 'plan' && <FilePlanTab canWrite={canWrite} />}
        {tab === 'schedules' && <SchedulesTab canWrite={canWrite} />}
        {tab === 'queue' && <QueueTab canWrite={canWrite} />}
      </div>
    </div>
  )
}

// ============================ Schedules ============================

function SchedulesTab({ canWrite }: { canWrite: boolean }) {
  const qc = useQueryClient()
  const { data: schedules = [] } = useQuery({ queryKey: ['record-schedules'], queryFn: listSchedules })
  const [draft, setDraft] = useState<Partial<RetentionSchedule>>({
    name: '', trigger_event: 'declaration', retention_period_days: 365, disposition_action: 'review',
  })

  const create = useAppMutation({
    mutationFn: () => createSchedule(draft),
    onSuccess: () => {
      toast.success('Schedule created')
      setDraft({ name: '', trigger_event: 'declaration', retention_period_days: 365, disposition_action: 'review' })
      qc.invalidateQueries({ queryKey: ['record-schedules'] })
    },
    defaultErrorMessage: 'Could not create schedule',
  })
  const remove = useAppMutation({
    mutationFn: (id: string) => deleteSchedule(id),
    onSuccess: () => { toast.success('Schedule deleted'); qc.invalidateQueries({ queryKey: ['record-schedules'] }) },
    defaultErrorMessage: 'Could not delete (still attached to a category?)',
  })

  return (
    <div className="space-y-4">
      {canWrite && (
        <div className="grid gap-2 rounded-lg border border-border p-3 sm:grid-cols-5" data-testid="schedule-builder">
          <input
            className="rounded border border-border bg-background px-2 py-1 text-sm sm:col-span-2"
            placeholder="Schedule name" value={draft.name ?? ''}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          />
          <select className="rounded border border-border bg-background px-2 py-1 text-sm"
            value={draft.trigger_event} onChange={(e) => setDraft({ ...draft, trigger_event: e.target.value as TriggerEvent })}>
            {TRIGGERS.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
          <input type="number" min={0}
            className="rounded border border-border bg-background px-2 py-1 text-sm"
            placeholder="days" value={draft.retention_period_days ?? 0}
            onChange={(e) => setDraft({ ...draft, retention_period_days: Number(e.target.value) })}
          />
          <select className="rounded border border-border bg-background px-2 py-1 text-sm"
            value={draft.disposition_action} onChange={(e) => setDraft({ ...draft, disposition_action: e.target.value as DispositionAction })}>
            {ACTIONS.map((a) => <option key={a} value={a}>{a}</option>)}
          </select>
          <Button size="sm" className="sm:col-span-5 sm:w-40" disabled={!draft.name || create.isPending}
            onClick={() => create.mutate(undefined)} data-testid="schedule-create">
            <Plus className="h-3 w-3" /> Add schedule
          </Button>
        </div>
      )}

      <div className="overflow-hidden rounded-lg border border-border">
        <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="bg-muted/40 text-start text-xs text-muted-foreground">
            <tr><th className="p-2">Name</th><th className="p-2">Trigger</th><th className="p-2">Retention</th><th className="p-2">Action</th><th /></tr>
          </thead>
          <tbody>
            {schedules.map((s) => (
              <tr key={s.id} className="border-t border-border">
                <td className="p-2 font-medium">{s.name}</td>
                <td className="p-2">{s.trigger_event}</td>
                <td className="p-2 tabular-nums">{s.retention_period_days} days</td>
                <td className="p-2">{s.disposition_action}</td>
                <td className="p-2 text-end">
                  {canWrite && (
                    <button onClick={() => remove.mutate(s.id)} className="text-destructive" aria-label="Delete schedule">
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  )}
                </td>
              </tr>
            ))}
            {schedules.length === 0 && <tr><td colSpan={5} className="p-3 text-center text-muted-foreground">No schedules yet.</td></tr>}
          </tbody>
        </table>
        </div>
      </div>
    </div>
  )
}

// ============================ File plan ============================

interface TreeNode extends RecordCategory { children: TreeNode[] }

function buildTree(flat: RecordCategory[]): TreeNode[] {
  const byId = new Map<string, TreeNode>()
  flat.forEach((c) => byId.set(c.id, { ...c, children: [] }))
  const roots: TreeNode[] = []
  byId.forEach((node) => {
    if (node.parent_id && byId.has(node.parent_id)) byId.get(node.parent_id)!.children.push(node)
    else roots.push(node)
  })
  return roots
}

function FilePlanTab({ canWrite }: { canWrite: boolean }) {
  const qc = useQueryClient()
  const { data: categories = [] } = useQuery({ queryKey: ['record-categories'], queryFn: listCategories })
  const { data: schedules = [] } = useQuery({ queryKey: ['record-schedules'], queryFn: listSchedules })
  const tree = useMemo(() => buildTree(categories), [categories])
  const [parentId, setParentId] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [code, setCode] = useState('')
  const [nodeType, setNodeType] = useState<NodeType>('category')

  const create = useAppMutation({
    mutationFn: () => createCategory({ parent_id: parentId, name, code, node_type: nodeType }),
    onSuccess: () => { toast.success('Node added'); setName(''); setCode(''); qc.invalidateQueries({ queryKey: ['record-categories'] }) },
    defaultErrorMessage: 'Could not add node',
  })
  const attach = useAppMutation({
    mutationFn: ({ id, scheduleId }: { id: string; scheduleId: string | null }) =>
      updateCategory(id, { retention_schedule_id: scheduleId }),
    onSuccess: () => { toast.success('Schedule attached'); qc.invalidateQueries({ queryKey: ['record-categories'] }) },
    defaultErrorMessage: 'Could not attach schedule',
  })
  const remove = useAppMutation({
    mutationFn: (id: string) => deleteCategory(id),
    onSuccess: () => { toast.success('Node deleted'); qc.invalidateQueries({ queryKey: ['record-categories'] }) },
    defaultErrorMessage: 'Could not delete (records declared in subtree?)',
  })

  return (
    <div className="grid gap-4 md:grid-cols-3">
      <div className="rounded-lg border border-border p-3 md:col-span-2" data-testid="file-plan-tree">
        {tree.length === 0 ? (
          <p className="text-sm text-muted-foreground">No file plan yet. Add a root category →</p>
        ) : (
          <ul className="space-y-1">{tree.map((n) => (
            <TreeRow key={n.id} node={n} depth={0} schedules={schedules} canWrite={canWrite}
              onAttach={(scheduleId) => attach.mutate({ id: n.id, scheduleId })}
              onDelete={() => remove.mutate(n.id)} onAddChild={() => setParentId(n.id)} />
          ))}</ul>
        )}
      </div>

      {canWrite && (
        <div className="space-y-2 rounded-lg border border-border p-3">
          <h3 className="text-sm font-semibold">Add node</h3>
          <select className="w-full rounded border border-border bg-background px-2 py-1 text-sm"
            value={parentId ?? ''} onChange={(e) => setParentId(e.target.value || null)}>
            <option value="">(root)</option>
            {categories.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </select>
          <input className="w-full rounded border border-border bg-background px-2 py-1 text-sm"
            placeholder="Node name" value={name} onChange={(e) => setName(e.target.value)} />
          <input className="w-full rounded border border-border bg-background px-2 py-1 text-sm"
            placeholder="Code (optional)" value={code} onChange={(e) => setCode(e.target.value)} />
          <select className="w-full rounded border border-border bg-background px-2 py-1 text-sm"
            value={nodeType} onChange={(e) => setNodeType(e.target.value as NodeType)}>
            <option value="category">category (branch)</option>
            <option value="series">series (leaf — declare against)</option>
          </select>
          <Button size="sm" className="w-full" disabled={!name || create.isPending} onClick={() => create.mutate(undefined)}
            data-testid="category-create">
            <Plus className="h-3 w-3" /> Add to file plan
          </Button>
        </div>
      )}
    </div>
  )
}

function TreeRow({ node, depth, schedules, canWrite, onAttach, onDelete, onAddChild }: {
  node: TreeNode; depth: number; schedules: RetentionSchedule[]; canWrite: boolean
  onAttach: (scheduleId: string | null) => void; onDelete: () => void; onAddChild: () => void
}) {
  const [open, setOpen] = useState(true)
  return (
    <li>
      <div className="flex items-center gap-2 rounded px-1 py-0.5 hover:bg-muted/40" style={{ paddingLeft: depth * 16 }}>
        {node.children.length > 0 ? (
          <button onClick={() => setOpen(!open)}><ChevronRight className={`h-3 w-3 transition-transform ${open ? 'rotate-90' : ''}`} /></button>
        ) : <FolderTree className="h-3 w-3 text-muted-foreground" />}
        <span className="text-sm font-medium">{node.name}</span>
        {node.code && <span className="text-xs text-muted-foreground">{node.code}</span>}
        <span className={`rounded px-1.5 text-[10px] uppercase ${node.node_type === 'series' ? 'bg-violet-500/15 text-violet-600' : 'bg-muted text-muted-foreground'}`}>{node.node_type}</span>
        {canWrite && (
          <span className="ms-auto flex items-center gap-2">
            <select className="rounded border border-border bg-background px-1 py-0.5 text-xs" value={node.retention_schedule_id ?? ''}
              onChange={(e) => onAttach(e.target.value || null)} aria-label="Attach schedule">
              <option value="">no schedule</option>
              {schedules.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
            <button onClick={onAddChild} aria-label="Add child"><Plus className="h-3 w-3" /></button>
            <button onClick={onDelete} className="text-destructive" aria-label="Delete node"><Trash2 className="h-3 w-3" /></button>
          </span>
        )}
      </div>
      {open && node.children.map((c) => (
        <ul key={c.id}><TreeRow node={c} depth={depth + 1} schedules={schedules} canWrite={canWrite}
          onAttach={(sid) => onAttach(sid)} onDelete={onDelete} onAddChild={onAddChild} /></ul>
      ))}
    </li>
  )
}

// ============================ Disposition queue ============================

function QueueTab({ canWrite }: { canWrite: boolean }) {
  const qc = useQueryClient()
  const [includeDeclared, setIncludeDeclared] = useState(false)
  const { data: rows = [] } = useQuery({
    queryKey: ['record-queue', includeDeclared],
    queryFn: () => dispositionQueue(includeDeclared),
  })

  const dispose = useAppMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) => disposeRecord(id, reason),
    onSuccess: () => {
      toast.success('Disposition certified — audit certificate written')
      qc.invalidateQueries({ queryKey: ['record-queue'] })
    },
    defaultErrorMessage: 'Could not dispose record',
  })

  return (
    <div className="space-y-3">
      <label className="flex items-center gap-2 text-sm text-muted-foreground">
        <input type="checkbox" checked={includeDeclared} onChange={(e) => setIncludeDeclared(e.target.checked)} />
        Include records not yet at cutoff
      </label>
      <div className="space-y-2">
        {rows.map((r) => (
          <QueueRow key={r.id} row={r} canWrite={canWrite}
            onDispose={(reason) => dispose.mutate({ id: r.id, reason })} busy={dispose.isPending} />
        ))}
        {rows.length === 0 && (
          <p className="rounded border border-dashed border-border p-4 text-center text-sm text-muted-foreground">
            Nothing awaiting disposition. The cutoff sweep flags records here once their retention expires.
          </p>
        )}
      </div>
    </div>
  )
}

function QueueRow({ row, canWrite, onDispose, busy }: {
  row: RecordRow; canWrite: boolean; onDispose: (reason: string) => void; busy: boolean
}) {
  const [confirming, setConfirming] = useState(false)
  const [reason, setReason] = useState('')
  const pending = row.disposition_state === 'cutoff_pending'
  return (
    <div className="rounded-lg border border-border p-3" data-testid={`queue-row-${row.id}`}>
      <div className="flex items-center gap-2 text-sm">
        <Clock className={`h-4 w-4 ${pending ? 'text-amber-500' : 'text-muted-foreground'}`} />
        <span className="font-medium">{row.document_title || row.document_id}</span>
        <span className="rounded bg-muted px-1.5 py-0.5 text-xs">{row.disposition_state}</span>
        {row.disposition_action && <span className="text-xs text-muted-foreground">→ {row.disposition_action}</span>}
        <span className="ms-auto text-xs text-muted-foreground">
          cutoff {formatDate(row.cutoff_date)}
        </span>
      </div>
      {canWrite && row.disposition_action !== 'permanent' && (
        confirming ? (
          <div className="mt-2 flex items-center gap-2">
            <input className="flex-1 rounded border border-border bg-background px-2 py-1 text-sm"
              placeholder="Disposition reason / authority" value={reason} onChange={(e) => setReason(e.target.value)} />
            <Button size="sm" disabled={busy} onClick={() => onDispose(reason)} data-testid="dispose-confirm">
              <ShieldCheck className="h-3 w-3" /> Certify disposition
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setConfirming(false)}>Cancel</Button>
          </div>
        ) : (
          <Button size="sm" variant="outline" className="mt-2" onClick={() => setConfirming(true)} data-testid="dispose-start">
            Review &amp; dispose
          </Button>
        )
      )}
    </div>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/records-retention?tab=records). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/records')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/records-retention', search: { tab: 'records' }, replace: true })
  },
})
