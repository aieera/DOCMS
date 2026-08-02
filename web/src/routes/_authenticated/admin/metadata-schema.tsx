import { useEffect, useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import {
  CheckCircle2, FileJson, ListPlus, Pencil, Plus, RotateCcw, Save, Trash2, XCircle,
} from 'lucide-react'

import { getMetadataSchema, updateMetadataSchema } from '@/api/metadataSchema'
import {
  describeSpec, listFields, removeField, toggleRequired, upsertField,
  type SchemaField,
} from '@/lib/schemaFields'
import { SchemaFieldDialog } from '@/components/admin/SchemaFieldDialog'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Skeleton } from '@/components/ui/Skeleton'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/shadcn/tabs'
import { cn } from '@/lib/cn'

// /admin/metadata-schema — tenant-wide JSON Schema for document custom
// fields, editable two ways:
//
//   Fields tab — visual builder (add / edit / delete / require). The
//     common path; no JSON knowledge needed.
//   JSON tab   — the raw schema for power users and paste-in migrations.
//
// ONE source of truth: the JSON text. The builder parses it, applies a
// pure operation from lib/schemaFields, and re-serialises. That keeps
// the two tabs from drifting apart and means every builder edit is
// immediately visible (and reversible) as JSON.

const EXAMPLE_SCHEMA = {
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  type: 'object',
  properties: {
    invoice_number: { type: 'string', description: 'e.g. INV-2026-0042' },
    amount_usd: { type: 'number', minimum: 0 },
    vendor: { type: 'string' },
    due_date: { type: 'string', format: 'date' },
    status: { type: 'string', enum: ['draft', 'approved', 'paid', 'void'] },
  },
  required: ['invoice_number', 'amount_usd'],
} as const

function MetadataSchemaPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'metadata-schema'],
    queryFn: getMetadataSchema,
  })

  const [text, setText] = useState('')
  const [dirty, setDirty] = useState(false)
  const [tab, setTab] = useState<'fields' | 'json'>('fields')
  const [editing, setEditing] = useState<SchemaField | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<SchemaField | null>(null)

  useEffect(() => {
    if (data !== undefined && !dirty) {
      setText(JSON.stringify(data && Object.keys(data).length > 0 ? data : EXAMPLE_SCHEMA, null, 2))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data])

  const parsed = useMemo<
    { ok: true; value: Record<string, unknown> } | { ok: false; error: string } | null
  >(() => {
    if (!text.trim()) return null
    try {
      const value = JSON.parse(text) as unknown
      if (value === null || typeof value !== 'object' || Array.isArray(value)) {
        return { ok: false, error: 'Schema root must be a JSON object' }
      }
      const obj = value as Record<string, unknown>
      if (obj.type !== undefined && obj.type !== 'object') {
        return { ok: false, error: 'Root `type` must be "object" (if set)' }
      }
      return { ok: true, value: obj }
    } catch (err) {
      return { ok: false, error: (err as Error).message }
    }
  }, [text])

  const schema = parsed?.ok ? parsed.value : null
  const fields = useMemo(() => listFields(schema), [schema])

  // Every builder action funnels through here: apply a pure op, write
  // the JSON back, mark dirty.
  const apply = (next: Record<string, unknown>) => {
    setText(JSON.stringify(next, null, 2))
    setDirty(true)
  }

  const save = useAppMutation({
    mutationFn: () => {
      if (!parsed || !parsed.ok) throw new Error('schema not valid')
      return updateMetadataSchema(parsed.value)
    },
    onSuccess: (saved) => {
      toast.success('Schema saved')
      setDirty(false)
      qc.setQueryData(['admin', 'metadata-schema'], saved)
    },
    onError: (err: unknown) => {
      const m = typeof err === 'object' && err && 'message' in err
        ? String((err as { message?: string }).message)
        : 'Save failed'
      toast.error(m)
    },
  })

  const reset = () => {
    if (dirty && !window.confirm('Discard unsaved changes?')) return
    setText(JSON.stringify(data && Object.keys(data).length > 0 ? data : EXAMPLE_SCHEMA, null, 2))
    setDirty(false)
  }

  const loadExample = () => {
    if (dirty && !window.confirm('Discard unsaved changes and load example?')) return
    setText(JSON.stringify(EXAMPLE_SCHEMA, null, 2))
    setDirty(true)
  }

  const openAdd = () => { setEditing(null); setDialogOpen(true) }
  const openEdit = (f: SchemaField) => { setEditing(f); setDialogOpen(true) }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-6">
      <PageHeader
        title="Metadata schema"
        description="Tenant-wide custom fields for documents. Every document validates its custom fields against this schema on save."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {dirty && (
              <Badge variant="outline" className="text-[11px]" data-testid="schema-dirty">
                Unsaved changes
              </Badge>
            )}
            <Button variant="outline" onClick={loadExample}>Load example</Button>
            <Button variant="outline" onClick={reset} disabled={!dirty}>
              <RotateCcw className="h-4 w-4" /> Reset
            </Button>
            <Button
              onClick={() => save.mutate()}
              disabled={!parsed || !parsed.ok}
              loading={save.isPending}
              data-testid="schema-save"
            >
              <Save className="h-4 w-4" /> Save
            </Button>
          </div>
        }
      />

      {isLoading ? (
        <Skeleton className="h-96" />
      ) : (
        <Tabs value={tab} onValueChange={(v) => setTab(v as 'fields' | 'json')} className="flex min-h-0 flex-1 flex-col">
          <TabsList className="self-start">
            <TabsTrigger value="fields" data-testid="tab-fields">
              <ListPlus className="me-1.5 h-4 w-4" /> Fields
              {fields.length > 0 && <span className="ms-1.5 text-xs text-muted-foreground">({fields.length})</span>}
            </TabsTrigger>
            <TabsTrigger value="json" data-testid="tab-json">
              <FileJson className="me-1.5 h-4 w-4" /> JSON
            </TabsTrigger>
          </TabsList>

          {/* ---- Visual builder ------------------------------------- */}
          <TabsContent value="fields" className="mt-4 min-h-0 flex-1">
            {!schema ? (
              <Card className="p-6 text-sm text-destructive" data-testid="fields-blocked">
                The JSON is invalid, so the field list can't be shown. Fix it on the
                JSON tab — the builder edits the same schema.
              </Card>
            ) : fields.length === 0 ? (
              <EmptyState
                icon={<ListPlus className="h-8 w-8" />}
                title="No custom fields yet"
                description="Add fields like invoice number, vendor, or due date. They appear on every document's metadata form and are validated on save."
                actionLabel="Add your first field"
                onAction={openAdd}
              />
            ) : (
              <div className="space-y-3">
                <div className="flex items-center justify-between gap-2">
                  <p className="text-sm text-muted-foreground">
                    {fields.length} field{fields.length === 1 ? '' : 's'} ·{' '}
                    {fields.filter((f) => f.required).length} required
                  </p>
                  <Button size="sm" onClick={openAdd} data-testid="add-field">
                    <Plus className="me-1 h-4 w-4" /> Add field
                  </Button>
                </div>
                <ul className="space-y-2" data-testid="field-list">
                  {fields.map((f) => {
                    const spec = ((schema.properties ?? {}) as Record<string, Record<string, unknown>>)[f.name] ?? {}
                    return (
                      <li key={f.name}>
                        <Card className="flex items-center gap-3 p-3">
                          <div className="min-w-0 flex-1">
                            <div className="flex flex-wrap items-center gap-2">
                              <span className="truncate font-mono text-sm font-medium">{f.name}</span>
                              <Badge variant="outline" className="text-[10px]">{describeSpec(spec)}</Badge>
                              {f.required && (
                                <Badge className="bg-destructive/10 text-[10px] text-destructive hover:bg-destructive/10">
                                  Required
                                </Badge>
                              )}
                            </div>
                            {f.description && (
                              <p className="mt-0.5 truncate text-xs text-muted-foreground">{f.description}</p>
                            )}
                          </div>
                          <button
                            type="button"
                            role="switch"
                            aria-checked={f.required}
                            aria-label={`${f.required ? 'Unmark' : 'Mark'} ${f.name} required`}
                            title={f.required ? 'Required — click to make optional' : 'Optional — click to require'}
                            onClick={() => apply(toggleRequired(schema, f.name))}
                            className={cn(
                              'relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors',
                              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
                              f.required ? 'bg-destructive' : 'bg-muted',
                            )}
                            data-testid={`require-${f.name}`}
                          >
                            <span
                              aria-hidden
                              className={cn(
                                'absolute top-0.5 inline-block h-4 w-4 rounded-full bg-background shadow-sm transition-[inset-inline-start]',
                                f.required ? 'start-[18px]' : 'start-0.5',
                              )}
                            />
                          </button>
                          <Button
                            variant="ghost" size="icon"
                            onClick={() => openEdit(f)}
                            aria-label={`Edit ${f.name}`}
                            data-testid={`edit-${f.name}`}
                          >
                            <Pencil className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost" size="icon"
                            onClick={() => setPendingDelete(f)}
                            aria-label={`Delete ${f.name}`}
                            data-testid={`delete-${f.name}`}
                          >
                            <Trash2 className="h-4 w-4 text-destructive" />
                          </Button>
                        </Card>
                      </li>
                    )
                  })}
                </ul>
                <p className="text-xs text-muted-foreground">
                  Changes are staged locally — press <strong className="text-foreground">Save</strong> to apply them tenant-wide.
                </p>
              </div>
            )}
          </TabsContent>

          {/* ---- Raw JSON ------------------------------------------- */}
          <TabsContent value="json" className="mt-4 min-h-0 flex-1">
            <div className="grid min-h-0 flex-1 gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
              <Card className="flex min-h-[55vh] flex-col overflow-hidden p-0">
                <div className="flex items-center justify-between gap-2 border-b border-border bg-muted/40 px-3 py-2 text-xs">
                  <div className="flex items-center gap-2">
                    <FileJson className="h-3.5 w-3.5 text-muted-foreground" />
                    <span className="font-medium">Schema (JSON)</span>
                  </div>
                  {parsed === null ? (
                    <span className="text-muted-foreground">empty</span>
                  ) : parsed.ok ? (
                    <span className="flex items-center gap-1 text-success"><CheckCircle2 className="h-3 w-3" /> valid JSON</span>
                  ) : (
                    <span className="flex items-center gap-1 text-destructive"><XCircle className="h-3 w-3" /> {parsed.error}</span>
                  )}
                </div>
                <textarea
                  spellCheck={false}
                  className={cn(
                    'min-h-0 w-full flex-1 resize-none border-0 bg-background p-3 font-mono text-xs leading-relaxed',
                    'focus:outline-none focus:ring-1 focus:ring-ring',
                    parsed && !parsed.ok && 'text-destructive',
                  )}
                  value={text}
                  onChange={(e) => { setText(e.target.value); setDirty(true) }}
                  data-testid="schema-json"
                />
              </Card>

              <Card className="space-y-2 self-start p-4 text-xs text-muted-foreground">
                <p>
                  <strong className="text-foreground">Draft 2020-12</strong> JSON Schema. Keep root{' '}
                  <code className="rounded bg-muted px-1 font-mono">type: "object"</code>.
                </p>
                <p className="text-foreground">Supported per-property:</p>
                <ul className="list-disc space-y-0.5 ps-5">
                  <li><code className="rounded bg-muted px-1 font-mono">type</code>: string, number, integer, boolean, array</li>
                  <li><code className="rounded bg-muted px-1 font-mono">enum</code>, <code className="rounded bg-muted px-1 font-mono">format</code>, <code className="rounded bg-muted px-1 font-mono">pattern</code></li>
                  <li><code className="rounded bg-muted px-1 font-mono">minimum</code>/<code className="rounded bg-muted px-1 font-mono">maximum</code>, <code className="rounded bg-muted px-1 font-mono">minLength</code>/<code className="rounded bg-muted px-1 font-mono">maxLength</code></li>
                  <li><code className="rounded bg-muted px-1 font-mono">required</code>: array at the root</li>
                </ul>
                <p className="pt-1">
                  Anything you edit here shows up on the Fields tab, and vice versa.
                  Documents whose custom fields fail this schema are rejected at
                  create or update time with a validation error.
                </p>
              </Card>
            </div>
          </TabsContent>
        </Tabs>
      )}

      <SchemaFieldDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        field={editing}
        takenNames={fields.map((f) => f.name)}
        onSubmit={(field, originalName) => apply(upsertField(schema, field, originalName))}
      />

      <ConfirmDialog
        open={!!pendingDelete}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title={pendingDelete ? `Delete field “${pendingDelete.name}”?` : 'Delete field'}
        description="New documents will no longer collect this field. Values already stored on existing documents are left untouched."
        confirmLabel="Delete field"
        destructive
        onConfirm={() => {
          if (pendingDelete && schema) apply(removeField(schema, pendingDelete.name))
          setPendingDelete(null)
        }}
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/metadata-schema')({
  component: MetadataSchemaPage,
})
