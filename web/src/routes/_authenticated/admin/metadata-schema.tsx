import { useEffect, useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { FileJson, Save, RotateCcw, CheckCircle2, XCircle } from 'lucide-react'

import { getMetadataSchema, updateMetadataSchema } from '@/api/metadataSchema'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'

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

  // Seed the editor when the server response arrives.
  useEffect(() => {
    if (data !== undefined && !dirty) {
      setText(JSON.stringify(data && Object.keys(data).length > 0 ? data : EXAMPLE_SCHEMA, null, 2))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data])

  // Live parse. Null = empty buffer; otherwise parsed object or error
  // message. The save button is enabled only on a green parse.
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

  const save = useMutation({
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
      const m =
        typeof err === 'object' && err && 'message' in err
          ? String((err as { message?: string }).message)
          : 'Save failed'
      toast.error(m)
    },
  })

  const reset = () => {
    if (dirty && !window.confirm('Discard unsaved changes?')) return
    setText(
      JSON.stringify(data && Object.keys(data).length > 0 ? data : EXAMPLE_SCHEMA, null, 2),
    )
    setDirty(false)
  }

  const loadExample = () => {
    if (dirty && !window.confirm('Discard unsaved changes and load example?')) return
    setText(JSON.stringify(EXAMPLE_SCHEMA, null, 2))
    setDirty(true)
  }

  return (
    <div>
      <PageHeader
        title="Metadata Schema"
        description="Tenant-wide JSON Schema for document custom fields. Documents validate their custom_metadata against this schema on every write."
        actions={
          <div className="flex gap-2">
            <Button onClick={loadExample}>Load example</Button>
            <Button onClick={reset} disabled={!dirty}>
              <RotateCcw className="h-4 w-4" /> Reset
            </Button>
            <Button
              onClick={() => save.mutate()}
              disabled={!parsed || !parsed.ok || save.isPending}
            >
              <Save className="h-4 w-4" />
              {save.isPending ? 'Saving…' : 'Save'}
            </Button>
          </div>
        }
      />

      {isLoading ? (
        <Skeleton className="h-96" />
      ) : (
        <div className="grid grid-cols-[1fr_320px] gap-4">
          <div>
            <div className="mb-2 flex items-center gap-2">
              <FileJson className="h-4 w-4" />
              <span className="text-sm font-medium">Schema (JSON)</span>
              {parsed === null ? (
                <span className="text-xs text-[var(--color-text-secondary)]">empty</span>
              ) : parsed.ok ? (
                <span className="flex items-center gap-1 text-xs text-emerald-600">
                  <CheckCircle2 className="h-3 w-3" />
                  valid JSON
                </span>
              ) : (
                <span className="flex items-center gap-1 text-xs text-red-600">
                  <XCircle className="h-3 w-3" />
                  {parsed.error}
                </span>
              )}
            </div>
            <textarea
              spellCheck={false}
              className={`h-[520px] w-full resize-none rounded-md border bg-[var(--color-bg)] p-3 font-mono text-xs leading-relaxed ${
                parsed && !parsed.ok
                  ? 'border-red-500 focus:border-red-500'
                  : 'border-[var(--color-border)]'
              }`}
              value={text}
              onChange={(e) => {
                setText(e.target.value)
                setDirty(true)
              }}
            />
          </div>

          <aside className="space-y-3">
            <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3 text-sm">
              <div className="mb-2 font-medium">Preview</div>
              <SchemaPreview schema={parsed?.ok ? parsed.value : null} />
            </div>
            <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3 text-xs text-[var(--color-text-secondary)] space-y-2">
              <p>
                <strong>Draft 2020-12</strong> JSON Schema. Keep root <code>type: "object"</code>.
              </p>
              <p>Supported validators on each property:</p>
              <ul className="list-disc pl-5">
                <li>
                  <code>type</code>: string, number, integer, boolean, array
                </li>
                <li>
                  <code>enum</code>, <code>format</code>, <code>pattern</code>
                </li>
                <li>
                  <code>minimum</code>, <code>maximum</code>, <code>minLength</code>,{' '}
                  <code>maxLength</code>
                </li>
                <li>
                  <code>required</code>: array at the root of property names
                </li>
              </ul>
              <p>
                Documents whose <code>custom_metadata</code> fails this schema are rejected at
                create/update time with a <code>VALIDATION</code> error.
              </p>
            </div>
          </aside>
        </div>
      )}
    </div>
  )
}

// SchemaPreview renders the properties list as a label-and-type
// summary so admins can scan the tenant's metadata contract without
// reading the raw JSON every time.
function SchemaPreview({ schema }: { schema: Record<string, unknown> | null }) {
  if (!schema) {
    return (
      <p className="text-xs text-[var(--color-text-secondary)]">
        Fix the JSON to see a field preview.
      </p>
    )
  }
  const properties = (schema.properties ?? {}) as Record<string, Record<string, unknown>>
  const required = new Set((schema.required as string[] | undefined) ?? [])
  const entries = Object.entries(properties)
  if (entries.length === 0) {
    return (
      <p className="text-xs text-[var(--color-text-secondary)]">
        No <code>properties</code> defined. Add them at the root of the schema.
      </p>
    )
  }
  return (
    <ul className="space-y-1">
      {entries.map(([name, spec]) => (
        <li key={name} className="flex items-start justify-between gap-2 text-xs">
          <div className="min-w-0">
            <div className="font-mono font-medium">
              {name}
              {required.has(name) && <span className="text-red-500"> *</span>}
            </div>
            {typeof spec.description === 'string' && (
              <div className="text-[var(--color-text-secondary)]">{spec.description}</div>
            )}
          </div>
          <div className="shrink-0 text-right text-[var(--color-text-secondary)]">
            <div className="font-mono">{describe(spec)}</div>
          </div>
        </li>
      ))}
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
