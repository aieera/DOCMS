// /clauses — clause library admin page (ADR 0104 §18 F11).
// Phase 1: list + free-text search + filter chips + create modal +
// inline edit/delete. Variation tracking, detection, and OnlyOffice
// side-panel are Phase 2-4 per the ADR.
import { copyText } from '@/lib/clipboard'
import { useEffect, useMemo, useState } from 'react'
import { Link, createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { CheckCircle2, Copy, Edit3, FileText, Plus, Search, Tag, Trash2, X } from 'lucide-react'
import { LabeledSelect } from '@/components/ui/shadcn/select'

import {
  approveClause,
  createClause,
  deleteClause,
  getClauseVariations,
  listClauses,
  patchClause,
  revokeClauseApproval,
  type Clause,
  type CreateClauseInput,
} from '@/api/clauses'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import { Dialog } from '@/components/ui/Dialog'
import { useAuthStore } from '@/store/authStore'

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
  // After creating a clause we only have its id; the full Clause arrives
  // with the next list refetch. Hold the id and auto-select it once it
  // shows up so the detail panel opens on the new clause instead of
  // staying on "No clause selected".
  const [pendingSelectId, setPendingSelectId] = useState<string | null>(null)
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role)
  // Same admin/owner gate the Approve/Revoke action uses elsewhere in the
  // app for clause-approval-adjacent surfaces.
  const canManageApproval = role === 'admin' || role === 'owner'

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

  const delMut = useAppMutation({
    mutationFn: (id: string) => deleteClause(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['clauses'] })
      setSelected(null)
      toast.success('Clause deleted')
    },
    onError: () => toast.error('Delete failed'),
  })

  // ADR 0104 approval — dedicated approve/revoke endpoints (Task 4),
  // replacing the earlier patchClause({ approved: true }) shortcut.
  const approveMut = useAppMutation({
    mutationFn: (id: string) => approveClause(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['clauses'] })
      // Keep the detail pane in sync immediately (same treatment the
      // Edit flow applies via onSaved → setSelected). approveClause
      // returns void, so stamp a client-side approved_at; `selected` is a
      // disconnected local copy that the ['clauses'] refetch never
      // touches, but only its truthiness (approved vs not) drives the UI
      // here, so this optimistic patch is sufficient and stays correct.
      setSelected((s) => (s ? { ...s, approved_at: new Date().toISOString() } : s))
      toast.success('Clause approved')
    },
    onError: () => toast.error('Approve failed'),
  })

  const revokeMut = useAppMutation({
    mutationFn: (id: string) => revokeClauseApproval(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['clauses'] })
      // Mirror the approve path: clear approval locally so the badge
      // and the Approve⇄Revoke toggle flip without a re-select.
      setSelected((s) => (s ? { ...s, approved_at: null, approved_by: null } : s))
      toast.success('Approval revoked')
    },
    onError: () => toast.error('Revoke failed'),
  })

  // Select the just-created clause once it lands in the refetched list.
  useEffect(() => {
    if (!pendingSelectId) return
    const found = data?.clauses.find((c) => c.id === pendingSelectId)
    if (found) {
      setSelected(found)
      setPendingSelectId(null)
    }
  }, [pendingSelectId, data])

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
            className="gap-2 shadow-neu-sm transition-[box-shadow,transform] duration-150 hover:-translate-y-px hover:shadow-neu"
          >
            <Plus className="h-4 w-4" /> New clause
          </Button>
        }
      />

      <section className="flex flex-wrap items-center gap-2">
        <div className="flex flex-1 items-center gap-2 rounded-md border border-input bg-muted px-2 shadow-neu-inset">
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
            canManage={canManageApproval}
            onApprove={() => approveMut.mutate(selected.id)}
            onRevoke={() => revokeMut.mutate(selected.id)}
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
        onCreated={(id) => {
          qc.invalidateQueries({ queryKey: ['clauses'] })
          setPendingSelectId(id)
        }}
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

// Radix can't hold an empty option value, so the "All …" clear row uses
// this sentinel, mapped back to '' (= no filter) on change.
const ALL_FILTER = '__all__'

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
  // Styled Radix dropdown — replaces the native <select> so the filter
  // controls match the rest of the app's selects and theme correctly in
  // dark mode (the native option list rendered light).
  return (
    <LabeledSelect
      value={value || ALL_FILTER}
      onValueChange={(v) => onChange(v === ALL_FILTER ? '' : v)}
      placeholder={placeholder}
      triggerClassName="w-auto min-w-[150px]"
      options={[
        { value: ALL_FILTER, label: placeholder },
        ...options.map((o) => ({ value: o, label: o })),
      ]}
    />
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
          <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs text-success">
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
  canManage,
  onApprove,
  onRevoke,
  onEdit,
  onDelete,
}: {
  clause: Clause
  onClose: () => void
  canManage: boolean
  onApprove: () => void
  onRevoke: () => void
  onEdit: () => void
  onDelete: () => void
}) {
  // ADR 0104 Phase 4 — variation tracking: how many documents use this
  // clause and how much each occurrence drifts from the canonical body.
  const { data: variations } = useQuery({
    queryKey: ['clause-variations', clause.id],
    queryFn: () => getClauseVariations(clause.id),
  })
  const docById = useMemo(
    () => new Map((variations?.documents ?? []).map((d) => [d.id, d])),
    [variations],
  )

  return (
    <article className="space-y-3 rounded-md border border-border bg-card p-4">
      <header className="flex items-start justify-between gap-2">
        <div>
          <div className="flex items-center gap-2">
            <h2 className="text-lg font-semibold">{clause.name}</h2>
            {clause.approved_at && (
              <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs text-success">
                ✓ Approved
              </span>
            )}
          </div>
          <p className="text-xs text-muted-foreground">
            v{clause.version} · {clause.jurisdiction || 'no jurisdiction'} · updated {new Date(clause.updated_at).toLocaleString()}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button
            variant="ghost"
            size="sm"
            aria-label="Copy clause"
            onClick={() =>
              copyText(clause.body_text)
                .then(() => toast.success('Clause copied'))
                .catch(() => toast.error('Copy failed'))
            }
          >
            <Copy className="h-3.5 w-3.5" />
          </Button>
          {/* Approve/Revoke — admin/owner only, mirroring the manage
              gate used for the admin-only actions elsewhere in the app. */}
          {canManage && (
            clause.approved_at ? (
              <Button variant="outline" size="sm" onClick={onRevoke}>
                Revoke
              </Button>
            ) : (
              <Button variant="outline" size="sm" onClick={onApprove}>
                <CheckCircle2 className="me-1 h-3.5 w-3.5" /> Approve
              </Button>
            )
          )}
          <button onClick={onClose} aria-label="close" className="text-muted-foreground hover:text-foreground">
            <X className="h-4 w-4" />
          </button>
        </div>
      </header>

      <div className="flex flex-wrap gap-1 text-xs">
        {clause.tags.map((t) => (
          <span key={t} className="inline-flex items-center gap-1 rounded-full bg-secondary px-2 py-0.5 text-secondary-foreground">
            <Tag className="h-3 w-3" />
            {t}
          </span>
        ))}
      </div>

      <div className="rounded-md bg-muted p-3 shadow-neu-inset">
        <pre className="whitespace-pre-wrap font-sans text-sm">{clause.body_text}</pre>
      </div>

      {/* Usage — ADR 0104 Phase 4 variation tracking across documents. */}
      <div className="space-y-2 border-t border-border pt-3">
        <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Usage</h3>
        {variations && variations.variations.length > 0 ? (
          <>
            <p className="text-xs text-muted-foreground">
              Used in {variations.total_documents} document{variations.total_documents === 1 ? '' : 's'}
            </p>
            <ul className="space-y-1.5">
              {variations.variations.map((v) => {
                const docs = v.document_ids
                  .map((id) => docById.get(id))
                  .filter((d): d is NonNullable<typeof d> => d != null)
                return (
                  <li key={v.normalized_hash} className="rounded-md border border-border/60 p-2 text-xs">
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-medium">{v.occurrences}×</span>
                      <span className="text-muted-foreground">
                        {Math.round(v.min_similarity * 100)}–{Math.round(v.max_similarity * 100)}%
                      </span>
                    </div>
                    <p className="mt-1 line-clamp-2 text-muted-foreground">{v.sample_text}</p>
                    {docs.length > 0 && (
                      <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1">
                        {docs.map((d) => (
                          <Link
                            key={d.id}
                            to="/workspaces/$workspaceId/documents/$documentId"
                            params={{ workspaceId: d.workspace_id, documentId: d.id }}
                            className="inline-flex items-center gap-1 text-primary hover:underline"
                          >
                            <FileText className="h-3 w-3" />
                            {d.title}
                          </Link>
                        ))}
                      </div>
                    )}
                  </li>
                )
              })}
            </ul>
          </>
        ) : (
          <p className="text-xs text-muted-foreground">No detected uses yet.</p>
        )}
      </div>

      <footer className="flex flex-wrap items-center gap-2 text-xs">
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
  onCreated: (id: string) => void
}) {
  const [form, setForm] = useState<CreateClauseInput>({ name: '', body_text: '', jurisdiction: '', tags: [] })
  const [tagText, setTagText] = useState('')
  const mut = useAppMutation({
    mutationFn: createClause,
    onSuccess: (created) => {
      toast.success('Clause created')
      onCreated(created.id)
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
            className="w-full rounded-md border border-input bg-muted p-2 text-sm font-mono shadow-neu-inset"
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

  const mut = useAppMutation({
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
            className="w-full rounded-md border border-input bg-muted p-2 text-sm font-mono shadow-neu-inset"
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
