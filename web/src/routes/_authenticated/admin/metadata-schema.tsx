import { useEffect, useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { CheckCircle2, FileJson, RotateCcw, Save, XCircle } from 'lucide-react'

import { getMetadataSchema, updateMetadataSchema } from '@/api/metadataSchema'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/Skeleton'
import { cn } from '@/lib/cn'

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

  // Friendly per-field "required" toggle in the preview rail. Reads
  // the current parsed schema, flips the field's presence in the
  // `required` array, re-serialises, and marks dirty. Disabled when
  // the JSON is invalid (we'd have nothing to modify). Preserves
  // existing property order and other keys.
  const toggleRequired = (fieldName: string) => {
    if (!parsed || !parsed.ok) return
    const next: Record<string, unknown> = { ...parsed.value }
    const current = Array.isArray(next.required) ? [...(next.required as string[])] : []
    const idx = current.indexOf(fieldName)
    if (idx >= 0) current.splice(idx, 1)
    else current.push(fieldName)
    if (current.length) next.required = current
    else delete next.required
    setText(JSON.stringify(next, null, 2))
    setDirty(true)
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-6">
      <PageHeader
        title="Metadata schema"
        description="Tenant-wide JSON Schema for document custom fields. Documents validate their custom fields against this schema on every save."
        actions={
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={loadExample}>Load example</Button>
            <Button variant="outline" onClick={reset} disabled={!dirty}>
              <RotateCcw className="h-4 w-4" /> Reset
            </Button>
            <Button onClick={() => save.mutate()} disabled={!parsed || !parsed.ok} loading={save.isPending}>
              <Save className="h-4 w-4" /> Save
            </Button>
          </div>
        }
      />

      {isLoading ? (
        <Skeleton className="h-96" />
      ) : (
        <div className="grid flex-1 min-h-0 gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
          <Card className="flex min-h-[60vh] flex-col overflow-hidden p-0 lg:min-h-0">
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
            />
          </Card>

          <aside className="flex flex-col gap-3 lg:sticky lg:top-20 lg:self-start lg:max-h-[calc(100vh-6rem)]">
            <Card className="flex min-h-0 flex-col p-4">
              <h3 className="mb-1 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Fields &amp; required
              </h3>
              <p className="mb-2 text-[11px] text-muted-foreground">
                Toggle the switch to mark a field required. Documents save-blocks until required fields have a value.
              </p>
              <div className="min-h-0 flex-1 overflow-y-auto pe-1">
                <SchemaPreview
                  schema={parsed?.ok ? parsed.value : null}
                  onToggleRequired={parsed?.ok ? toggleRequired : undefined}
                />
              </div>
            </Card>
            <Card className="space-y-2 p-4 text-xs text-muted-foreground">
              <p>
                <strong className="text-foreground">Draft 2020-12</strong> JSON Schema. Keep root <code className="rounded bg-muted px-1 font-mono">type: "object"</code>.
              </p>
              <p className="text-foreground">Supported per-property:</p>
              <ul className="list-disc space-y-0.5 ps-5">
                <li><code className="rounded bg-muted px-1 font-mono">type</code>: string, number, integer, boolean, array</li>
                <li><code className="rounded bg-muted px-1 font-mono">enum</code>, <code className="rounded bg-muted px-1 font-mono">format</code>, <code className="rounded bg-muted px-1 font-mono">pattern</code></li>
                <li><code className="rounded bg-muted px-1 font-mono">minimum</code>/<code className="rounded bg-muted px-1 font-mono">maximum</code>, <code className="rounded bg-muted px-1 font-mono">minLength</code>/<code className="rounded bg-muted px-1 font-mono">maxLength</code></li>
                <li><code className="rounded bg-muted px-1 font-mono">required</code>: array at the root</li>
              </ul>
              <p className="pt-1">
                Documents whose custom fields fail this schema are rejected at create or update time with a validation error.
              </p>
            </Card>
          </aside>
        </div>
      )}
    </div>
  )
}

function SchemaPreview({
  schema,
  onToggleRequired,
}: {
  schema: Record<string, unknown> | null
  onToggleRequired?: (name: string) => void
}) {
  if (!schema) {
    return <p className="text-xs text-muted-foreground">Fix the JSON to see a field preview.</p>
  }
  const properties = (schema.properties ?? {}) as Record<string, Record<string, unknown>>
  const required = new Set((schema.required as string[] | undefined) ?? [])
  const entries = Object.entries(properties)
  if (entries.length === 0) {
    return (
      <p className="text-xs text-muted-foreground">
        No <code className="rounded bg-muted px-1 font-mono">properties</code> defined. Add them at the root of the schema.
      </p>
    )
  }
  return (
    <ul className="space-y-1">
      {entries.map(([name, spec]) => {
        const isRequired = required.has(name)
        return (
          <li
            key={name}
            className="flex items-start justify-between gap-2 rounded-md border border-transparent p-1.5 text-xs transition-colors hover:border-border hover:bg-accent/40"
          >
            <div className="min-w-0 flex-1">
              <div className="font-mono font-medium">
                {name}
                {isRequired && <span className="text-destructive" aria-label="required"> *</span>}
              </div>
              {typeof spec.description === 'string' && (
                <div className="line-clamp-2 text-muted-foreground">{spec.description}</div>
              )}
            </div>
            <div className="flex shrink-0 flex-col items-end gap-1.5">
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">{describe(spec)}</code>
              {onToggleRequired ? (
                <button
                  type="button"
                  role="switch"
                  aria-checked={isRequired}
                  aria-label={`${isRequired ? 'Unmark' : 'Mark'} ${name} required`}
                  onClick={() => onToggleRequired(name)}
                  className={cn(
                    'relative inline-flex h-4 w-7 shrink-0 rounded-full transition-colors',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
                    isRequired ? 'bg-destructive' : 'bg-muted',
                  )}
                  title={isRequired ? 'Required — click to unmark' : 'Optional — click to require'}
                >
                  <span
                    aria-hidden
                    className={cn(
                      'absolute top-0.5 inline-block h-3 w-3 rounded-full bg-background shadow-sm transition-[left]',
                      isRequired ? 'left-[14px]' : 'left-0.5',
                    )}
                  />
                </button>
              ) : null}
            </div>
          </li>
        )
      })}
    </ul>
  )
}

function describe(spec: Record<string, unknown>): string {
  const t = typeof spec.type === 'string' ? spec.type : 'any'
  if (Array.isArray(spec.enum)) return `enum(${spec.enum.length})`
  if (spec.format) return `${t}/${String(spec.format)}`
  return t
}

export const Route = createFileRoute('/_authenticated/admin/metadata-schema')({
  component: MetadataSchemaPage,
})
