import { Trans, useTranslation } from 'react-i18next'
import { formatDateTime } from '@/lib/formatters'
import { PasswordInput } from '@/components/ui/PasswordInput'
import { useEffect, useState } from 'react'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
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
import { ErrorState } from '@/components/ui/ErrorState'

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

export function NERConfigPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ['ner-config'],
    queryFn: getNERConfig,
  })

  const [draft, setDraft] = useState<NERConfig | null>(null)
  useEffect(() => {
    if (data && !draft) setDraft(data)
  }, [data, draft])

  const dirty = draft !== null && data !== undefined && JSON.stringify(draft) !== JSON.stringify(data)

  const save = useAppMutation({
    mutationFn: (patch: Partial<NERConfig>) => updateNERConfig(patch),
    onSuccess: (saved) => {
      qc.setQueryData(['ner-config'], saved)
      setDraft(saved)
      toast.success(t('adm.ner.saved'))
    },
    onError: (err: unknown) => {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? t('adm.ner.err_save'))
    },
  })

  if (isError) {
    // Includes the license-gated 403 ("intel_llm feature is not
    // included") — surface it in-page instead of an eternal spinner.
    return <ErrorState message={t('adm.ner.err_load')} onRetry={() => void refetch()} />
  }
  if (isLoading || !draft) {
    return (
      <div>
        <PageHeader variant="section" title={t('adm.ner.title')} description={t('adm.ner.loading')} />
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
    <div>
      <PageHeader variant="section"
        title={t('adm.ner.title')}
        description={t('adm.ner.description')}
        actions={
          <Button
            size="sm"
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate(draft)}
          >
            {save.isPending ? t('adm.ner.saving') : t('adm.ner.save')}
          </Button>
        }
      />

      <section className="mt-6 rounded border border-border">
        <header className="flex items-center gap-2 border-b border-border bg-card px-4 py-2 text-sm font-medium">
          <Brain className="h-4 w-4 text-success" />
          {t('adm.ner.llm_tier')}
        </header>
        <div className="grid grid-cols-1 gap-4 p-4 text-sm md:grid-cols-2">
          <Toggle
            label={t('adm.ner.enable')}
            help={t('adm.ner.enable_help')}
            checked={draft.llm_enabled}
            onChange={(v) => setDraft({ ...draft, llm_enabled: v })}
          />
          <ModelField
            value={draft.llm_model}
            onChange={(v) => setDraft({ ...draft, llm_model: v })}
          />
          <NumField
            label={t('adm.ner.batch')}
            help={t('adm.ner.batch_help')}
            value={draft.llm_batch_size}
            min={1}
            max={20}
            step={1}
            onChange={(v) => setDraft({ ...draft, llm_batch_size: v })}
          />
          <NumField
            label={t('adm.ner.min_conf')}
            help={t('adm.ner.min_conf_help')}
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
          {t('adm.ner.entity_types')}
        </header>
        <div className="grid grid-cols-2 gap-3 p-4 text-sm md:grid-cols-3">
          {LLM_TYPE_OPTIONS.map((ty) => (
            <label key={ty} className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={draft.llm_entity_types.includes(ty)}
                onChange={() => toggleType(ty)}
              />
              <span dir="ltr" className="font-mono text-xs">{ty}</span>
            </label>
          ))}
        </div>
        <p className="border-t border-border px-4 py-2 text-xs text-muted-foreground">
          {t('adm.ner.entity_note')}
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
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const [draft, setDraft] = useState('')

  const save = useAppMutation({
    mutationFn: (k: string) => setNERAPIKey(k),
    onSuccess: () => {
      toast.success(t('adm.ner.key_saved'))
      setDraft('')
      qc.invalidateQueries({ queryKey: ['ner-config'] })
    },
    onError: (err: unknown) => {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? t('adm.ner.err_key_save'))
    },
  })

  const clear = useAppMutation({
    mutationFn: clearNERAPIKey,
    onSuccess: () => {
      toast.success(t('adm.ner.key_cleared'))
      qc.invalidateQueries({ queryKey: ['ner-config'] })
    },
    onError: () => toast.error(t('adm.ner.err_key_clear')),
  })

  const ollama = modelHint.startsWith('ollama/')

  return (
    <section className="mt-6 rounded border border-border">
      <header className="flex items-center gap-2 border-b border-border bg-card px-4 py-2 text-sm font-medium">
        <KeyRound className="h-4 w-4 text-warning" />
        {t('adm.ner.provider_key')}
      </header>

      <div className="space-y-4 p-4 text-sm">
        {hasKey ? (
          <div className="flex items-center justify-between gap-3 rounded border border-success/40 bg-success/10 px-3 py-2 text-xs">
            <span className="flex items-center gap-2 text-success">
              <CheckCircle2 className="h-4 w-4" />
              {setAt ? t('adm.ner.configured_at', { when: formatDateTime(setAt) }) : t('adm.ner.configured')}
            </span>
            <Button
              size="sm"
              variant="ghost"
              disabled={clear.isPending}
              onClick={() => {
                if (window.confirm(t('adm.ner.clear_confirm'))) {
                  clear.mutate()
                }
              }}
              aria-label={t('adm.ner.clear_aria')}
            >
              <Trash2 className="me-1 h-3 w-3" /> {t('adm.ner.clear')}
            </Button>
          </div>
        ) : (
          <div className="rounded border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warning-strong">
            {t('adm.ner.no_key')}
          </div>
        )}

        {ollama ? (
          <p className="text-xs text-muted-foreground">
            <Trans
              i18nKey="adm.ner.ollama_note"
              ns="common"
              components={[<code key="e" dir="ltr" />]}
            />
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
                {hasKey ? t('adm.ner.replace_key') : t('adm.ner.paste_key')}
              </span>
              <PasswordInput
                dir="ltr"
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
                {save.isPending ? t('adm.ner.saving') : t('adm.ner.save_key')}
              </Button>
            </div>
          </form>
        )}

        <p className="text-xs text-muted-foreground">
          {t('adm.ner.key_note')}
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
  const { t } = useTranslation('common')
  const isPreset = MODEL_PRESETS.some((p) => p.value === value)
  const [custom, setCustom] = useState(!isPreset)
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs uppercase tracking-wide text-muted-foreground">
        {t('adm.ner.model')}
      </span>
      {custom ? (
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          dir="ltr"
          placeholder={t('adm.ner.custom_ph')}
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
        {custom ? t('adm.ner.use_preset') : t('adm.ner.custom_model')}
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

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/ai?tab=ner). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/intelligence/ner-config')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/ai', search: { tab: 'ner' }, replace: true })
  },
})
