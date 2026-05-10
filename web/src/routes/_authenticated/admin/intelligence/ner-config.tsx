import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Brain, CheckCircle2, KeyRound, Trash2 } from 'lucide-react'

import {
  clearNERAPIKey,
  getNERConfig,
  setNERAPIKey,
  updateNERConfig,
  type NERConfig,
} from '@/api/ner'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'

// Mirrors the LLM-eligible types in services/document/internal/service/
// ner_service.go::allowedEntityTypes, minus the deterministic ones
// (regex/SpaCy already handle email, phone, ssn, dob, credit_card,
// icd_code, cpt_code, currency, name, amount).
const LLM_TYPE_OPTIONS = [
  'party_name', 'effective_date', 'jurisdiction', 'governing_law',
  'account_number', 'tax_id', 'patient_id', 'address', 'national_id',
] as const

// litellm provider strings — the user can type a custom one but these
// are the ones we recommend in ADR 0078.
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
        description="Per-tenant LLM tier for the entity types regex + SpaCy can't reach (ADR 0078). Off by default; enable only if your tenant has the right data-handling agreement with the chosen model provider."
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

      <section className="mt-6 rounded border border-border">
        <header className="flex items-center gap-2 border-b border-border bg-card px-4 py-2 text-sm font-medium">
          <Brain className="h-4 w-4 text-success" />
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

      <section className="mt-6 rounded border border-border">
        <header className="border-b border-border bg-card px-4 py-2 text-sm font-medium">
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
        <p className="border-t border-border px-4 py-2 text-xs text-muted-foreground">
          Types not listed (email, phone, SSN, DOB, credit_card, ICD-10, CPT) are handled deterministically by regex/SpaCy and never sent to the LLM.
        </p>
      </section>

      <APIKeySection
        hasKey={data?.has_api_key ?? false}
        setAt={data?.api_key_set_at}
        modelHint={draft.llm_model}
      />
    </div>
  )
}

function APIKeySection({
  hasKey,
  setAt,
  modelHint,
}: {
  hasKey: boolean
  setAt?: string
  modelHint: string
}) {
  const qc = useQueryClient()
  const [draft, setDraft] = useState('')

  const save = useMutation({
    mutationFn: (k: string) => setNERAPIKey(k),
    onSuccess: () => {
      toast.success('API key saved')
      setDraft('')
      qc.invalidateQueries({ queryKey: ['ner-config'] })
    },
    onError: (err: unknown) => {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? 'Failed to save key')
    },
  })

  const clear = useMutation({
    mutationFn: clearNERAPIKey,
    onSuccess: () => {
      toast.success('API key cleared')
      qc.invalidateQueries({ queryKey: ['ner-config'] })
    },
    onError: () => toast.error('Failed to clear key'),
  })

  const ollama = modelHint.startsWith('ollama/')

  return (
    <section className="mt-6 rounded border border-border">
      <header className="flex items-center gap-2 border-b border-border bg-card px-4 py-2 text-sm font-medium">
        <KeyRound className="h-4 w-4 text-warning" />
        Provider API key
      </header>

      <div className="space-y-4 p-4 text-sm">
        {hasKey ? (
          <div className="flex items-center justify-between gap-3 rounded border border-success/40 bg-success/10 px-3 py-2 text-xs">
            <span className="flex items-center gap-2 text-success">
              <CheckCircle2 className="h-4 w-4" />
              Configured{setAt ? ` · set ${new Date(setAt).toLocaleString()}` : ''}
            </span>
            <Button
              size="sm"
              variant="ghost"
              disabled={clear.isPending}
              onClick={() => {
                if (window.confirm('Clear the saved API key? The LLM tier will silently no-op until a new key is set.')) {
                  clear.mutate()
                }
              }}
              aria-label="Clear API key"
            >
              <Trash2 className="mr-1 h-3 w-3" /> Clear
            </Button>
          </div>
        ) : (
          <div className="rounded border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warning dark:">
            No key configured. The LLM tier silently skips when enabled without a key — only regex + SpaCy results land in the database.
          </div>
        )}

        {ollama ? (
          <p className="text-xs text-muted-foreground">
            Ollama runs against your own server — no API key needed. Make sure
            <code> OLLAMA_BASE_URL </code> is reachable from the intelligence
            worker container.
          </p>
        ) : (
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              if (draft.trim().length >= 16) save.mutate(draft.trim())
            }}
          >
            <label className="flex flex-col gap-1">
              <span className="text-xs uppercase tracking-wide text-muted-foreground">
                {hasKey ? 'Replace key' : 'Paste new key'}
              </span>
              <Input
                type="password"
                placeholder={modelHint.startsWith('claude-') ? 'sk-ant-…' : 'sk-…'}
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                autoComplete="off"
              />
            </label>
            <div className="flex justify-end">
              <Button
                type="submit"
                size="sm"
                disabled={save.isPending || draft.trim().length < 16}
              >
                {save.isPending ? 'Saving…' : 'Save key'}
              </Button>
            </div>
          </form>
        )}

        <p className="text-xs text-muted-foreground">
          Stored encrypted (AES-256-GCM) under the deployment KEK. Never returned
          to the UI in plaintext after save. The intelligence worker decrypts
          per-request when calling the LLM provider.
        </p>
      </div>
    </section>
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
        {help && <div className="text-xs text-muted-foreground">{help}</div>}
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
      <span className="text-xs uppercase tracking-wide text-muted-foreground">
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
        className="self-start text-xs text-primary hover:underline"
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
      <span className="text-xs uppercase tracking-wide text-muted-foreground">
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
      {help && <span className="text-xs text-muted-foreground">{help}</span>}
    </label>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/ner-config')({
  component: NERConfigPage,
})
