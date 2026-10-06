import { useEffect, useMemo, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
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
  const { t, i18n } = useTranslation('common')
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
        return { ok: false, error: t('adm.meta.err_root_object') }
      }
      const obj = value as Record<string, unknown>
      if (obj.type !== undefined && obj.type !== 'object') {
        return { ok: false, error: t('adm.meta.err_root_type') }
      }
      return { ok: true, value: obj }
    } catch (err) {
      return { ok: false, error: (err as Error).message }
    }
  }, [text, t])

  const schema = parsed?.ok ? parsed.value : null
  const fields = useMemo(() => listFields(schema), [schema])
  // describeSpec() resolves through the catalogue, so the badges below must
  // re-render when the locale lands (it arrives with /auth/me).
  const specLocale = i18n.language

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
      toast.success(t('adm.meta.saved'))
      setDirty(false)
      qc.setQueryData(['admin', 'metadata-schema'], saved)
    },
    onError: (err: unknown) => {
      const m = typeof err === 'object' && err && 'message' in err
        ? String((err as { message?: string }).message)
        : t('adm.meta.err_save')
      toast.error(m)
    },
  })

  const reset = () => {
    if (dirty && !window.confirm(t('adm.meta.confirm_discard'))) return
    setText(JSON.stringify(data && Object.keys(data).length > 0 ? data : EXAMPLE_SCHEMA, null, 2))
    setDirty(false)
  }

  const loadExample = () => {
    if (dirty && !window.confirm(t('adm.meta.confirm_discard_example'))) return
    setText(JSON.stringify(EXAMPLE_SCHEMA, null, 2))
    setDirty(true)
  }

  const openAdd = () => { setEditing(null); setDialogOpen(true) }
  const openEdit = (f: SchemaField) => { setEditing(f); setDialogOpen(true) }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-6">
      <PageHeader
        title={t('adm.meta.title')}
        description={t('adm.meta.description')}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {dirty && (
              <Badge variant="outline" className="text-[11px]" data-testid="schema-dirty">
                {t('adm.meta.dirty')}
              </Badge>
            )}
            <Button variant="outline" onClick={loadExample}>{t('adm.meta.load_example')}</Button>
            <Button variant="outline" onClick={reset} disabled={!dirty}>
              <RotateCcw className="h-4 w-4" /> {t('adm.meta.reset')}
            </Button>
            <Button
              onClick={() => save.mutate()}
              disabled={!parsed || !parsed.ok}
              loading={save.isPending}
              data-testid="schema-save"
            >
              <Save className="h-4 w-4" /> {t('adm.meta.save')}
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
              <ListPlus className="me-1.5 h-4 w-4" /> {t('adm.meta.tab_fields')}
              {fields.length > 0 && <span className="ms-1.5 text-xs text-muted-foreground">({fields.length})</span>}
            </TabsTrigger>
            <TabsTrigger value="json" data-testid="tab-json">
              <FileJson className="me-1.5 h-4 w-4" /> {t('adm.meta.tab_json')}
            </TabsTrigger>
          </TabsList>

          {/* ---- Visual builder ------------------------------------- */}
          <TabsContent value="fields" className="mt-4 min-h-0 flex-1">
            {!schema ? (
              <Card className="p-6 text-sm text-destructive" data-testid="fields-blocked">
                {t('adm.meta.blocked')}
              </Card>
            ) : fields.length === 0 ? (
              <EmptyState
                icon={<ListPlus className="h-8 w-8" />}
                title={t('adm.meta.empty')}
                description={t('adm.meta.empty_desc')}
                actionLabel={t('adm.meta.empty_action')}
                onAction={openAdd}
              />
            ) : (
              <div className="space-y-3">
                <div className="flex items-center justify-between gap-2">
                  <p className="text-sm text-muted-foreground">
                    {t('adm.meta.count', { count: fields.length, required: fields.filter((f) => f.required).length })}
                  </p>
                  <Button size="sm" onClick={openAdd} data-testid="add-field">
                    <Plus className="me-1 h-4 w-4" /> {t('adm.meta.add_field')}
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
                              <span dir="ltr" className="truncate font-mono text-sm font-medium">{f.name}</span>
                              <Badge variant="outline" className="text-[10px]" key={specLocale}>{describeSpec(spec)}</Badge>
                              {f.required && (
                                <Badge className="bg-destructive/10 text-[10px] text-foreground hover:bg-destructive/10">
                                  {t('adm.meta.required')}
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
                            aria-label={f.required ? t('adm.meta.unmark_required', { name: f.name }) : t('adm.meta.mark_required', { name: f.name })}
                            title={f.required ? t('adm.meta.required_hint') : t('adm.meta.optional_hint')}
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
                                'absolute top-0.5 inline-block h-4 w-4 rounded-full bg-background shadow-neu-sm transition-[inset-inline-start]',
                                f.required ? 'start-[18px]' : 'start-0.5',
                              )}
                            />
                          </button>
                          <Button
                            variant="ghost" size="icon"
                            onClick={() => openEdit(f)}
                            aria-label={t('adm.meta.edit_field', { name: f.name })}
                            data-testid={`edit-${f.name}`}
                          >
                            <Pencil className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost" size="icon"
                            onClick={() => setPendingDelete(f)}
                            aria-label={t('adm.meta.delete_field', { name: f.name })}
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
                  <Trans i18nKey="adm.meta.staged" ns="common" components={[<strong key="s" className="text-foreground" />]} />
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
                    <span className="font-medium">{t('adm.meta.json_title')}</span>
                  </div>
                  {parsed === null ? (
                    <span className="text-muted-foreground">{t('adm.meta.json_empty')}</span>
                  ) : parsed.ok ? (
                    <span className="flex items-center gap-1 text-success"><CheckCircle2 className="h-3 w-3" /> {t('adm.meta.json_valid')}</span>
                  ) : (
                    <span className="flex items-center gap-1 text-destructive"><XCircle className="h-3 w-3" /> {parsed.error}</span>
                  )}
                </div>
                <textarea
                  spellCheck={false}
                  className={cn(
                    'min-h-0 w-full flex-1 resize-none border-0 bg-muted p-3 font-mono text-xs leading-relaxed shadow-neu-inset',
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
                  {/* <Trans>: a bold term and a literal identifier sit
                      mid-sentence, which Arabic reorders around. */}
                  <Trans
                    i18nKey="adm.meta.help_root"
                    ns="common"
                    components={[
                      <strong key="d" className="text-foreground" />,
                      <code key="t" dir="ltr" className="rounded bg-muted px-1 font-mono" />,
                    ]}
                  />
                </p>
                <p className="text-foreground">{t('adm.meta.help_supported')}</p>
                {/* Each bullet mixes JSON Schema keywords — which stay
                    Latin and dir="ltr" — with prose Arabic reorders. */}
                <ul className="list-disc space-y-0.5 ps-5">
                  {(['help_type', 'help_enum', 'help_range', 'help_required'] as const).map((k) => (
                    <li key={k}>
                      <Trans
                        i18nKey={`adm.meta.${k}`}
                        ns="common"
                        components={Array.from({ length: 4 }, (_, i) => (
                          <code key={i} dir="ltr" className="rounded bg-muted px-1 font-mono" />
                        ))}
                      />
                    </li>
                  ))}
                </ul>
                <p className="pt-1">{t('adm.meta.help_tail')}</p>
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
        title={pendingDelete ? t('adm.meta.del_title', { name: pendingDelete.name }) : t('adm.meta.del_fallback')}
        description={t('adm.meta.del_desc')}
        confirmLabel={t('adm.meta.del_confirm')}
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
