// /clauses — clause library admin page (ADR 0104 §18 F11).
// Phase 1: list + free-text search + filter chips + create modal +
// inline edit/delete. Variation tracking, detection, and OnlyOffice
// side-panel are Phase 2-4 per the ADR.
import { useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { CheckCircle2, Edit3, FileText, Plus, Search, Tag, Trash2, X } from 'lucide-react'

import {
  createClause,
  deleteClause,
  listClauses,
  patchClause,
  type Clause,
  type CreateClauseInput,
} from '@/api/clauses'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import { Dialog } from '@/components/ui/Dialog'

export const Route = createFileRoute('/_authenticated/clauses/')({
  component: ClausesPage,
})

function ClausesPage() {
  const [q, setQ] = useState('')
  const [jurisdictionFilter, setJurisdictionFilter] = useState('')
  const [tagFilter, setTagFilter] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<Clause | null>(null)
  const [selected, setSelected] = useState<Clause | null>(null)
  const qc = useQueryClient()

  const { data, isLoading } = useQuery({
    queryKey: ['clauses', q, jurisdictionFilter, tagFilter],
    queryFn: () => listClauses({
      q: q || undefined,
      jurisdiction: jurisdictionFilter || undefined,
      tag: tagFilter || undefined,
      limit: 100,
    }),
    staleTime: 15_000,
  })

  const delMut = useMutation({
    mutationFn: (id: string) => deleteClause(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['clauses'] })
      setSelected(null)
      toast.success('Clause deleted')
    },
    onError: () => toast.error('Delete failed'),
  })

  const approveMut = useMutation({
    mutationFn: (id: string) => patchClause(id, { approved: true }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['clauses'] })
      toast.success('Clause approved')
    },
    onError: () => toast.error('Approve failed'),
  })

  // Unique tag + jurisdiction sets for filter chips.
  const { allTags, allJurisdictions } = useMemo(() => {
    const ts = new Set<string>()
    const js = new Set<string>()
    for (const c of data?.clauses ?? []) {
      for (const t of c.tags) ts.add(t)
      if (c.jurisdiction) js.add(c.jurisdiction)
    }
    return { allTags: [...ts].sort(), allJurisdictions: [...js].sort() }
  }, [data])

  return (
    <div className="space-y-4 p-6">
      <PageHeader
        title="Clause library"
        description="Manage reusable contract clauses for your organization. Create, edit, and organize standard clauses for use across your documents."
        actions={
          <Button
            onClick={() => setCreateOpen(true)}
            className="gap-2 shadow-sm transition-[box-shadow,transform] duration-150 hover:-translate-y-px hover:shadow-md"
          >
            <Plus className="h-4 w-4" /> New clause
          </Button>
        }
      />

      <section className="flex flex-wrap items-center gap-2">
        <div className="flex flex-1 items-center gap-2 rounded-md border border-border bg-card px-2">
          <Search className="h-4 w-4 text-muted-foreground" />
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search name, body, tags…"
            className="border-0 bg-transparent focus-visible:ring-0"
          />
        </div>
        <FilterSelect
          value={jurisdictionFilter}
          onChange={setJurisdictionFilter}
          options={allJurisdictions}
          placeholder="All jurisdictions"
        />
        <FilterSelect
          value={tagFilter}
          onChange={setTagFilter}
          options={allTags}
          placeholder="All tags"
        />
      </section>

      <section className="grid grid-cols-1 gap-4 lg:grid-cols-[1fr_1.2fr]">
        <div className="space-y-2">
          {isLoading && <Spinner />}
          {!isLoading && data?.clauses.length === 0 && (
            <p className="rounded-md border border-dashed border-border p-6 text-center text-sm text-muted-foreground">
              No clauses yet. Click "New clause" to seed your library.
            </p>
          )}
          {data?.clauses.map((c) => (
            <ClauseCard
              key={c.id}
              clause={c}
              active={selected?.id === c.id}
              onClick={() => setSelected(c)}
            />
          ))}
        </div>

        {selected ? (
          <ClauseDetail
            clause={selected}
            onClose={() => setSelected(null)}
            onApprove={() => approveMut.mutate(selected.id)}
            onEdit={() => setEditing(selected)}
            onDelete={() => {
              if (confirm(`Delete "${selected.name}"?`)) delMut.mutate(selected.id)
            }}
          />
        ) : (
          <div className="flex flex-col items-center gap-3 rounded-md border border-dashed border-border p-12 text-center">
            <span className="flex h-12 w-12 items-center justify-center rounded-full bg-muted text-muted-foreground">
              <FileText className="h-6 w-6" />
            </span>
            <p className="text-sm font-medium">No clause selected</p>
            <p className="max-w-xs text-xs text-muted-foreground">
              Pick a clause on the left to preview its body, jurisdiction, tags, and version.
            </p>
          </div>
        )}
      </section>

      <CreateClauseDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={() => qc.invalidateQueries({ queryKey: ['clauses'] })}
      />

      {editing && (
        <EditClauseDialog
          clause={editing}
          onClose={() => setEditing(null)}
          onSaved={(updated) => {
            qc.invalidateQueries({ queryKey: ['clauses'] })
            // Keep the detail pane in sync with the new version.
            setSelected(updated)
            setEditing(null)
          }}
        />
      )}
    </div>
  )
}

function FilterSelect({
  value,
  onChange,
  options,
  placeholder,
}: {
  value: string
  onChange: (v: string) => void
  options: string[]
  placeholder: string
}) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="rounded-md border border-border bg-card px-2 py-1.5 text-sm"
    >
      <option value="">{placeholder}</option>
      {options.map((o) => <option key={o} value={o}>{o}</option>)}
    </select>
  )
}

function ClauseCard({ clause, active, onClick }: { clause: Clause; active: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className={`flex w-full flex-col gap-1 rounded-md border p-3 text-start text-sm transition-colors ${
        active ? 'border-primary/50 bg-primary/10' : 'border-border bg-card hover:bg-accent'
      }`}
    >
      <div className="flex items-start justify-between gap-2">
        <span className="font-semibold">{clause.name}</span>
      </div>
      <p className="line-clamp-2 text-xs text-muted-foreground">{clause.body_text}</p>
      {/* Unified metadata strip: jurisdiction → tags → version → status,
          all at consistent size/shape so the row reads as one band. */}
      <div className="flex flex-wrap items-center gap-1 text-[11px]">
        {clause.jurisdiction && (
          <span className="rounded-full border border-border px-2 py-0.5 text-xs">{clause.jurisdiction}</span>
        )}
        {clause.tags.slice(0, 4).map((t) => (
          <span key={t} className="inline-flex items-center gap-1 rounded-full border border-border px-2 py-0.5 text-xs">
            <Tag className="h-3 w-3" /> {t}
          </span>
        ))}
        <span className="rounded-full border border-border bg-muted/40 px-2 py-0.5 font-mono text-xs text-muted-foreground">
          v{clause.version}
        </span>
        {clause.approved_at && (
          <span className="inline-flex items-center gap-1 rounded-full bg-emerald-500/15 px-2 py-0.5 text-xs text-emerald-700 dark:text-emerald-300">
            <CheckCircle2 className="h-3 w-3" /> Approved
          </span>
        )}
      </div>
    </button>
  )
}

function ClauseDetail({
  clause,
  onClose,
  onApprove,
  onEdit,
  onDelete,
}: {
  clause: Clause
  onClose: () => void
  onApprove: () => void
  onEdit: () => void
  onDelete: () => void
}) {
  return (
    <article className="space-y-3 rounded-md border border-border bg-card p-4">
      <header className="flex items-start justify-between gap-2">
        <div>
          <h2 className="text-lg font-semibold">{clause.name}</h2>
          <p className="text-xs text-muted-foreground">
            v{clause.version} · {clause.jurisdiction || 'no jurisdiction'} · updated {new Date(clause.updated_at).toLocaleString()}
          </p>
        </div>
        <button onClick={onClose} aria-label="close" className="text-muted-foreground hover:text-foreground">
          <X className="h-4 w-4" />
        </button>
      </header>

      <div className="flex flex-wrap gap-1 text-xs">
        {clause.tags.map((t) => (
          <span key={t} className="inline-flex items-center gap-1 rounded-full bg-secondary px-2 py-0.5 text-secondary-foreground">
            <Tag className="h-3 w-3" />
            {t}
          </span>
        ))}
      </div>

      <div className="rounded-md border border-border bg-background p-3">
        <pre className="whitespace-pre-wrap font-sans text-sm">{clause.body_text}</pre>
      </div>

      <footer className="flex flex-wrap items-center gap-2 text-xs">
        {!clause.approved_at && (
          <Button variant="outline" size="sm" onClick={onApprove}>
            <CheckCircle2 className="me-1 h-3.5 w-3.5" /> Approve
          </Button>
        )}
        <Button variant="outline" size="sm" onClick={onEdit}>
          <Edit3 className="me-1 h-3.5 w-3.5" /> Edit
        </Button>
        <Button variant="outline" size="sm" onClick={onDelete}>
          <Trash2 className="me-1 h-3.5 w-3.5" /> Delete
        </Button>
        <span className="ms-auto text-muted-foreground">
          <FileText className="me-0.5 inline h-3 w-3" />
          {clause.body_text.length} chars
        </span>
      </footer>
    </article>
  )
}

function CreateClauseDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  onCreated: () => void
}) {
  const [form, setForm] = useState<CreateClauseInput>({ name: '', body_text: '', jurisdiction: '', tags: [] })
  const [tagText, setTagText] = useState('')
  const mut = useMutation({
    mutationFn: createClause,
    onSuccess: () => {
      toast.success('Clause created')
      onCreated()
      onOpenChange(false)
      setForm({ name: '', body_text: '', jurisdiction: '', tags: [] })
      setTagText('')
    },
    onError: () => toast.error('Create failed'),
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="New clause">
      <div className="space-y-3">
        <Input
          label="Name"
          value={form.name}
          onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
          placeholder="Indemnification — mutual"
        />
        <div className="space-y-1">
          <label className="text-xs font-semibold text-muted-foreground">Body</label>
          <textarea
            value={form.body_text}
            onChange={(e) => setForm((f) => ({ ...f, body_text: e.target.value }))}
            placeholder="Each party shall indemnify and hold harmless…"
            rows={8}
            className="w-full rounded-md border border-border bg-background p-2 text-sm font-mono"
          />
        </div>
        <Input
          label="Jurisdiction (optional)"
          value={form.jurisdiction ?? ''}
          onChange={(e) => setForm((f) => ({ ...f, jurisdiction: e.target.value }))}
          placeholder="US-CA, EU, …"
        />
        <Input
          label="Tags (comma-separated)"
          value={tagText}
          onChange={(e) => {
            setTagText(e.target.value)
            setForm((f) => ({ ...f, tags: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) }))
          }}
          placeholder="indemnification, mutual, vendor"
        />
        <Button
          onClick={() => {
            if (mut.isPending) return
            if (!form.name.trim()) { toast.error('Name is required'); return }
            if (!form.body_text.trim()) { toast.error('Body is required'); return }
            mut.mutate(form)
          }}
          disabled={mut.isPending}
          className="w-full"
        >
          {mut.isPending ? 'Creating…' : 'Create clause'}
        </Button>
      </div>
    </Dialog>
  )
}

// EditClauseDialog — wires PATCH /api/v1/clauses/{id}. PATCHing any of
// name/body_text/jurisdiction/tags auto-bumps the clause's version on
// the backend (see clauses_handler.go), so the parent invalidates the
// list query on save to pick up the new version stamp.
function EditClauseDialog({
  clause,
  onClose,
  onSaved,
}: {
  clause: Clause
  onClose: () => void
  onSaved: (updated: Clause) => void
}) {
  const [form, setForm] = useState({
    name:         clause.name,
    body_text:    clause.body_text,
    jurisdiction: clause.jurisdiction,
    tags:         clause.tags,
  })
  const [tagText, setTagText] = useState(clause.tags.join(', '))

  const mut = useMutation({
    mutationFn: () => patchClause(clause.id, {
      // Only send changed fields. The backend handles the bump
      // regardless, but skipping unchanged fields keeps the audit
      // row tighter and avoids gratuitous version churn if the
      // server later starts no-op'ing identical patches.
      ...(form.name         !== clause.name         && { name:         form.name }),
      ...(form.body_text    !== clause.body_text    && { body_text:    form.body_text }),
      ...(form.jurisdiction !== clause.jurisdiction && { jurisdiction: form.jurisdiction }),
      ...(!sameTags(form.tags, clause.tags)         && { tags:         form.tags }),
    }),
    onSuccess: (updated) => {
      toast.success(`Clause saved — now v${updated.version}`)
      onSaved(updated)
    },
    onError: () => toast.error('Save failed'),
  })

  const dirty =
    form.name         !== clause.name ||
    form.body_text    !== clause.body_text ||
    form.jurisdiction !== clause.jurisdiction ||
    !sameTags(form.tags, clause.tags)

  return (
    <Dialog
      open
      onOpenChange={(o) => { if (!o) onClose() }}
      title={`Edit "${clause.name}"`}
      description={`Saving bumps the clause to v${clause.version + 1}. Approval resets — approvers will need to re-approve.`}
      size="lg"
    >
      <div className="space-y-3">
        <Input
          label="Name"
          value={form.name}
          onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
        />
        <div className="space-y-1">
          <label className="text-xs font-semibold text-muted-foreground">Body</label>
          <textarea
            value={form.body_text}
            onChange={(e) => setForm((f) => ({ ...f, body_text: e.target.value }))}
            rows={10}
            className="w-full rounded-md border border-border bg-background p-2 text-sm font-mono"
          />
        </div>
        <Input
          label="Jurisdiction"
          value={form.jurisdiction}
          onChange={(e) => setForm((f) => ({ ...f, jurisdiction: e.target.value }))}
          placeholder="US-CA, EU, …"
        />
        <Input
          label="Tags (comma-separated)"
          value={tagText}
          onChange={(e) => {
            setTagText(e.target.value)
            setForm((f) => ({ ...f, tags: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) }))
          }}
        />
        <div className="flex items-center justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose} disabled={mut.isPending}>Cancel</Button>
          <Button onClick={() => mut.mutate()} disabled={!dirty || !form.name || !form.body_text || mut.isPending}>
            {mut.isPending ? 'Saving…' : 'Save'}
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function sameTags(a: string[], b: string[]) {
  if (a.length !== b.length) return false
  const sa = [...a].sort()
  const sb = [...b].sort()
  return sa.every((v, i) => v === sb[i])
}
