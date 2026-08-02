// Shared dirty-guarded settings card for the two intelligence config
// surfaces that had backend endpoints but no UI (smart-routing-config,
// anomaly-config — the admin hub card literally promised "rules +
// config"). Generic over the config shape: callers describe fields,
// this handles fetch, draft state, dirty tracking, save, revert, and
// the loading/error states its sibling pages historically lacked.
import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Save, Undo2 } from 'lucide-react'

import { useAppMutation } from '@/hooks/useAppMutation'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/Skeleton'

export type ConfigFieldKind = 'toggle' | 'number' | 'text'

export interface ConfigField<T> {
  key: keyof T & string
  label: string
  kind: ConfigFieldKind
  hint?: string
  /** number fields: input constraints */
  min?: number
  max?: number
  step?: number
}

export interface IntelligenceConfigCardProps<T extends object> {
  title: string
  description: string
  queryKey: readonly unknown[]
  fetchConfig: () => Promise<T>
  saveConfig: (patch: Partial<T>) => Promise<T>
  fields: ConfigField<T>[]
  /** Hide entirely when the caller knows the viewer can't write. */
  canEdit: boolean
  testid: string
}

export function IntelligenceConfigCard<T extends object>({
  title,
  description,
  queryKey,
  fetchConfig,
  saveConfig,
  fields,
  canEdit,
  testid,
}: IntelligenceConfigCardProps<T>) {
  const qc = useQueryClient()
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: [...queryKey],
    queryFn: fetchConfig,
    enabled: canEdit,
  })
  const [draft, setDraft] = useState<T | null>(null)

  useEffect(() => {
    if (data) setDraft(data)
  }, [data])

  const save = useAppMutation({
    mutationFn: (patch: Partial<T>) => saveConfig(patch),
    onSuccess: (next) => {
      qc.setQueryData([...queryKey], next)
      setDraft(next)
      toast.success('Configuration saved')
    },
    defaultErrorMessage: 'Could not save the configuration',
  })

  // Writes are admin/owner-gated server-side; rendering the form for a
  // read-only viewer would just farm 403 toasts.
  if (!canEdit) return null

  const dirty = !!data && !!draft && JSON.stringify(draft) !== JSON.stringify(data)

  return (
    <section
      className="rounded-md border border-border bg-card p-4"
      aria-label={title}
      data-testid={testid}
    >
      <div className="mb-3">
        <h2 className="text-sm font-semibold">{title}</h2>
        <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
      </div>

      {isError ? (
        <ErrorState message={`Could not load ${title.toLowerCase()}.`} onRetry={() => void refetch()} />
      ) : isLoading || !draft ? (
        <div className="space-y-2">
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
        </div>
      ) : (
        <>
          <div className="grid gap-3 sm:grid-cols-2">
            {fields.map((f) => (
              <ConfigFieldControl key={f.key} field={f} draft={draft} onChange={setDraft} />
            ))}
          </div>
          <div className="mt-4 flex items-center justify-end gap-2">
            {dirty && (
              <Button size="sm" variant="ghost" onClick={() => setDraft(data as T)}>
                <Undo2 className="me-1 h-3.5 w-3.5" /> Revert
              </Button>
            )}
            <Button
              size="sm"
              disabled={!dirty || save.isPending}
              onClick={() => save.mutate(draft as Partial<T>)}
              data-testid={`${testid}-save`}
            >
              <Save className="me-1 h-3.5 w-3.5" /> Save
            </Button>
          </div>
        </>
      )}
    </section>
  )
}

function ConfigFieldControl<T extends object>({
  field,
  draft,
  onChange,
}: {
  field: ConfigField<T>
  draft: T
  onChange: (next: T) => void
}) {
  const value = draft[field.key]
  const set = (v: unknown) => onChange({ ...draft, [field.key]: v })

  if (field.kind === 'toggle') {
    return (
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={Boolean(value)}
          onChange={(e) => set(e.target.checked)}
          className="mt-0.5"
        />
        <span>
          {field.label}
          {field.hint && <span className="block text-xs text-muted-foreground">{field.hint}</span>}
        </span>
      </label>
    )
  }

  return (
    <label className="block space-y-1 text-sm">
      <span className="font-medium">{field.label}</span>
      <Input
        type={field.kind === 'number' ? 'number' : 'text'}
        value={String(value ?? '')}
        min={field.min}
        max={field.max}
        step={field.step}
        onChange={(e) =>
          set(field.kind === 'number' ? Number(e.target.value) : e.target.value)
        }
      />
      {field.hint && <span className="block text-xs text-muted-foreground">{field.hint}</span>}
    </label>
  )
}
