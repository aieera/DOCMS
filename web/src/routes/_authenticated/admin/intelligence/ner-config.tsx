import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Brain } from 'lucide-react'

import { getNERConfig, updateNERConfig, type NERConfig } from '@/api/ner'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Select } from '@/components/ui/Select'

// Mirrors the LLM-eligible types in services/document/internal/service/
// ner_service.go::allowedEntityTypes, minus the deterministic ones
// (regex/SpaCy already handle email, phone, ssn, dob, credit_card,
// icd_code, cpt_code, currency, name, amount).
const LLM_TYPE_OPTIONS = [
  'party_name', 'effective_date', 'jurisdiction', 'governing_law',
  'account_number', 'tax_id', 'patient_id', 'address', 'national_id',
] as const

// litellm provider strings — the user can type a custom one but these
// are the ones we recommend in ADR 0061.
const MODEL_PRESETS = [
  { value: 'claude-haiku-4-5',     label: 'Claude Haiku 4.5 (recommended; BAA-eligible)' },
  { value: 'claude-sonnet-4-6',    label: 'Claude Sonnet 4.6 (slower, higher quality)' },
  { value: 'openai/gpt-4o-mini',   label: 'OpenAI GPT-4o-mini (cheapest cloud)' },
  { value: 'ollama/llama3.1:8b',   label: 'Ollama Llama 3.1 8B (self-hosted)' },
]

function NERConfigPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['ner-config'],
    queryFn: getNERConfig,
  })

  const [draft, setDraft] = useState<NERConfig | null>(null)
  useEffect(() => {
    if (data && !draft) setDraft(data)
  }, [data, draft])

  const dirty = draft !== null && data !== undefined && JSON.stringify(draft) !== JSON.stringify(data)

  const save = useMutation({
    mutationFn: (patch: Partial<NERConfig>) => updateNERConfig(patch),
    onSuccess: (saved) => {
      qc.setQueryData(['ner-config'], saved)
      setDraft(saved)
      toast.success('NER config saved')
    },
    onError: (err: unknown) => {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? 'Save failed')
    },
  })

  if (isLoading || !draft) {
    return (
      <div className="mx-auto max-w-3xl p-6">
        <PageHeader title="NER configuration" description="Loading…" />
      </div>
    )
  }

  const toggleType = (t: string) => {
    const has = draft.llm_entity_types.includes(t)
    setDraft({
      ...draft,
      llm_entity_types: has
        ? draft.llm_entity_types.filter((x) => x !== t)
        : [...draft.llm_entity_types, t],
    })
  }

  return (
    <div className="mx-auto max-w-3xl p-6">
      <PageHeader
        title="NER configuration"
        description="Per-tenant LLM tier for the entity types regex + SpaCy can't reach (ADR 0061). Off by default; enable only if your tenant has the right data-handling agreement with the chosen model provider."
        actions={
          <Button
            size="sm"
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate(draft)}
          >
            {save.isPending ? 'Saving…' : 'Save'}
          </Button>
        }
      />

      <section className="mt-6 rounded border border-[var(--color-border)]">
        <header className="flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-4 py-2 text-sm font-medium">
          <Brain className="h-4 w-4 text-emerald-500" />
          LLM tier
        </header>
        <div className="grid grid-cols-1 gap-4 p-4 text-sm md:grid-cols-2">
          <Toggle
            label="Enable LLM NER"
            help="When off, the pipeline runs regex + SpaCy only."
            checked={draft.llm_enabled}
            onChange={(v) => setDraft({ ...draft, llm_enabled: v })}
          />
          <ModelField
            value={draft.llm_model}
            onChange={(v) => setDraft({ ...draft, llm_model: v })}
          />
          <NumField
            label="Batch size"
            help="Max documents packed into one LLM request (1–20)."
            value={draft.llm_batch_size}
            min={1}
            max={20}
            step={1}
            onChange={(v) => setDraft({ ...draft, llm_batch_size: v })}
          />
          <NumField
            label="Min confidence"
            help="LLM-emitted entities below this score are dropped."
            value={draft.llm_min_confidence}
            min={0}
            max={1}
            step={0.05}
            onChange={(v) => setDraft({ ...draft, llm_min_confidence: v })}
          />
        </div>
      </section>

      <section className="mt-6 rounded border border-[var(--color-border)]">
        <header className="border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-4 py-2 text-sm font-medium">
          Entity types sent to the LLM
        </header>
        <div className="grid grid-cols-2 gap-3 p-4 text-sm md:grid-cols-3">
          {LLM_TYPE_OPTIONS.map((t) => (
            <label key={t} className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={draft.llm_entity_types.includes(t)}
                onChange={() => toggleType(t)}
              />
              <span className="font-mono text-xs">{t}</span>
            </label>
          ))}
        </div>
        <p className="border-t border-[var(--color-border)] px-4 py-2 text-xs text-[var(--color-text-secondary)]">
          Types not listed (email, phone, SSN, DOB, credit_card, ICD-10, CPT) are handled deterministically by regex/SpaCy and never sent to the LLM.
        </p>
      </section>

      <p className="mt-6 text-xs text-[var(--color-text-secondary)]">
        The LLM provider key is read from the intelligence worker's environment
        (<code>ANTHROPIC_API_KEY</code> for Claude, <code>OPENAI_API_KEY</code> for GPT-4o-mini,
        Ollama runs key-less). Toggling LLM on without a key configured is a no-op — the call fails
        gracefully and only regex+SpaCy results are persisted.
      </p>
    </div>
  )
}

function Toggle({
  label,
  help,
  checked,
  onChange,
}: {
  label: string
  help?: string
  checked: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <label className="flex cursor-pointer items-start gap-3">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        className="mt-1"
      />
      <div>
        <div className="font-medium">{label}</div>
        {help && <div className="text-xs text-[var(--color-text-secondary)]">{help}</div>}
      </div>
    </label>
  )
}

function ModelField({
  value,
  onChange,
}: {
  value: string
  onChange: (v: string) => void
}) {
  const isPreset = MODEL_PRESETS.some((p) => p.value === value)
  const [custom, setCustom] = useState(!isPreset)
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs uppercase tracking-wide text-[var(--color-text-secondary)]">
        Model (litellm provider/model)
      </span>
      {custom ? (
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="e.g. ollama/llama3.1:8b"
        />
      ) : (
        <Select
          value={value}
          onValueChange={onChange}
          options={MODEL_PRESETS}
        />
      )}
      <button
        type="button"
        className="self-start text-xs text-[var(--color-primary)] hover:underline"
        onClick={() => setCustom((c) => !c)}
      >
        {custom ? 'Use preset' : 'Custom model id'}
      </button>
    </div>
  )
}

function NumField({
  label,
  help,
  value,
  min,
  max,
  step,
  onChange,
}: {
  label: string
  help?: string
  value: number
  min: number
  max: number
  step: number
  onChange: (v: number) => void
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-xs uppercase tracking-wide text-[var(--color-text-secondary)]">
        {label}
      </span>
      <Input
        type="number"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => {
          const n = Number(e.target.value)
          if (!Number.isNaN(n)) onChange(n)
        }}
      />
      {help && <span className="text-xs text-[var(--color-text-secondary)]">{help}</span>}
    </label>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/ner-config')({
  component: NERConfigPage,
})
