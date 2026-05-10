import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Save } from 'lucide-react'

import {
  getAutoTagConfig,
  updateAutoTagConfig,
  type AutoTagConfig,
} from '@/api/intelligence'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'

const SOURCE_KEYS = ['ner', 'classification', 'llm', 'pattern'] as const

function AutoTagAdminPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['auto-tag-config'],
    queryFn: getAutoTagConfig,
  })
  const [draft, setDraft] = useState<AutoTagConfig | null>(null)
  const [blockedTagsInput, setBlockedTagsInput] = useState('')

  useEffect(() => {
    if (data && !draft) {
      setDraft(data)
      setBlockedTagsInput((data.blocked_tags ?? []).join(', '))
    }
  }, [data, draft])

  const save = useMutation({
    mutationFn: (patch: Partial<AutoTagConfig>) => updateAutoTagConfig(patch),
    onSuccess: (next) => {
      qc.setQueryData(['auto-tag-config'], next)
      setDraft(next)
      toast.success('Auto-tag config saved')
    },
    onError: () => toast.error('Save failed'),
  })

  if (isLoading || !draft) {
    return <div className="p-6 text-sm text-muted-foreground">Loading…</div>
  }

  const onSubmit = () => {
    const blocked = blockedTagsInput
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    if (draft.auto_apply_threshold < draft.suggest_threshold) {
      toast.error('auto_apply_threshold must be ≥ suggest_threshold')
      return
    }
    save.mutate({ ...draft, blocked_tags: blocked })
  }

  return (
    <div className="mx-auto max-w-3xl p-6">
      <PageHeader
        title="Auto-tagging"
        description="Per-tenant thresholds for the intelligence-driven tag suggestions pipeline (ADR 0052)."
      />

      <div className="mt-6 space-y-6 rounded border border-border p-5">
        <Toggle
          label="Enable auto-tagging"
          checked={draft.enabled}
          onChange={(v) => setDraft({ ...draft, enabled: v })}
        />

        <ThresholdRow
          label="Auto-apply threshold"
          help="Tags at or above this confidence are added to documents.tags automatically."
          value={draft.auto_apply_threshold}
          onChange={(v) => setDraft({ ...draft, auto_apply_threshold: v })}
        />
        <ThresholdRow
          label="Suggest threshold"
          help="Below this confidence, candidates are dropped entirely. Between this and auto-apply, they appear as pending suggestions."
          value={draft.suggest_threshold}
          onChange={(v) => setDraft({ ...draft, suggest_threshold: v })}
        />

        <NumberRow
          label="Max tags per document"
          min={1}
          max={100}
          value={draft.max_tags_per_document}
          onChange={(v) => setDraft({ ...draft, max_tags_per_document: v })}
        />

        <div>
          <label className="block text-sm font-medium">Blocked tags</label>
          <p className="mt-1 text-xs text-muted-foreground">
            Comma-separated. Tags listed here are filtered out before persistence.
          </p>
          <Input
            value={blockedTagsInput}
            onChange={(e) => setBlockedTagsInput(e.target.value)}
            placeholder="draft, internal-use-only"
            className="mt-2"
          />
        </div>

        <div>
          <div className="text-sm font-medium">Source weights</div>
          <p className="mt-1 text-xs text-muted-foreground">
            Multipliers applied to the model's raw confidence before threshold comparison.
          </p>
          <div className="mt-3 grid grid-cols-2 gap-3">
            {SOURCE_KEYS.map((src) => {
              const w = (draft.source_weights ?? {})[src] ?? 0
              return (
                <ThresholdRow
                  key={src}
                  label={src}
                  value={Number(w)}
                  onChange={(v) =>
                    setDraft({
                      ...draft,
                      source_weights: { ...draft.source_weights, [src]: v },
                    })
                  }
                />
              )
            })}
          </div>
        </div>

        <div className="flex justify-end pt-2">
          <Button onClick={onSubmit} disabled={save.isPending}>
            <Save className="mr-2 h-4 w-4" />
            Save
          </Button>
        </div>
      </div>
    </div>
  )
}

function Toggle({
  label,
  checked,
  onChange,
}: {
  label: string
  checked: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <label className="flex items-center justify-between text-sm">
      <span>{label}</span>
      <input
        type="checkbox"
        className="h-4 w-4"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
    </label>
  )
}

function ThresholdRow({
  label,
  help,
  value,
  onChange,
}: {
  label: string
  help?: string
  value: number
  onChange: (v: number) => void
}) {
  return (
    <div>
      <label className="flex items-center justify-between text-sm">
        <span className="capitalize">{label}</span>
        <span className="tabular-nums text-muted-foreground">{value.toFixed(2)}</span>
      </label>
      {help && <p className="mt-1 text-xs text-muted-foreground">{help}</p>}
      <input
        type="range"
        min={0}
        max={1}
        step={0.05}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="mt-2 w-full"
      />
    </div>
  )
}

function NumberRow({
  label,
  min,
  max,
  value,
  onChange,
}: {
  label: string
  min: number
  max: number
  value: number
  onChange: (v: number) => void
}) {
  return (
    <div>
      <label className="block text-sm font-medium">{label}</label>
      <Input
        type="number"
        min={min}
        max={max}
        value={value}
        onChange={(e) => onChange(Math.max(min, Math.min(max, Number(e.target.value) || min)))}
        className="mt-2 w-32"
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/auto-tag')({
  component: AutoTagAdminPage,
})
