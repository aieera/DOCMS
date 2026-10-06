import { Trans, useTranslation } from 'react-i18next'
import { PasswordInput } from '@/components/ui/PasswordInput'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { AlertTriangle, Beaker, Coins, KeyRound, ShieldAlert } from 'lucide-react'

import {
  getTenantLLMConfig,
  updateTenantLLMConfig,
  testLLMCompletion,
  type TenantLLMConfig,
  type TenantLLMConfigPatch,
} from '@/api/llm-config'
import { getLLMUsage } from '@/api/llm-usage'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Spinner } from '@/components/ui/Spinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { formatRelativeTime, formatUsd } from '@/lib/formatters'

const PROVIDER_OPTIONS = [
  { value: 'anthropic',  label: 'Anthropic (Claude)' },
  { value: 'gemini',     label: 'Google (Gemini)' },
  { value: 'openai',     label: 'OpenAI (GPT)' },
  { value: 'bedrock',    label: 'AWS Bedrock' },
  { value: 'vllm_local', label: 'vLLM (self-hosted, on-prem)' },
  { value: 'custom',     label: 'Custom (advanced)' },
]

// US-domiciled providers that don't trigger the export-control banner.
// vLLM is the on-prem path; bedrock is AWS US (regional config is the
// admin's responsibility and out of scope here). Anthropic itself is
// headquartered in San Francisco — the original list omitted it which
// raised a false-positive "not US-domiciled" warning every time an
// admin picked the platform's default provider.
const US_PROVIDERS = new Set(['vllm_local', 'openai', 'bedrock', 'anthropic', 'gemini'])

const PROVIDER_DEFAULT_MODELS: Record<string, string[]> = {
  anthropic:  ['anthropic/claude-haiku-4-5', 'anthropic/claude-sonnet-4-6', 'anthropic/claude-opus-4-7'],
  gemini:     ['gemini/gemini-2.5-flash', 'gemini/gemini-2.5-pro', 'gemini/gemini-2.0-flash'],
  openai:     ['openai/gpt-4o-mini', 'openai/gpt-4o', 'openai/gpt-4-turbo'],
  bedrock:    ['bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0'],
  vllm_local: ['vllm/meta-llama/Llama-3.1-70B-Instruct', 'vllm/mistralai/Mistral-Large-Instruct-2407'],
  custom:     [],
}

export function TenantAIPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ['tenant-llm-config'],
    queryFn: getTenantLLMConfig,
  })
  const { data: usage } = useQuery({
    queryKey: ['llm-usage'],
    queryFn: getLLMUsage,
    refetchInterval: 30_000,
  })

  const [draft, setDraft] = useState<TenantLLMConfig | null>(null)
  // Plaintext key never round-trips — we only hold it in component
  // state until the user clicks Save, then send it once.
  const [pendingKey, setPendingKey] = useState<string>('')
  const [testPrompt, setTestPrompt] = useState<string>(() => t('adm.aip.default_prompt'))
  const [testResult, setTestResult] = useState<string>('')

  useEffect(() => {
    if (data) setDraft(data)
  }, [data])

  const saveMut = useAppMutation({
    mutationFn: (patch: TenantLLMConfigPatch) => updateTenantLLMConfig(patch),
    onSuccess: (next) => {
      qc.setQueryData(['tenant-llm-config'], next)
      setPendingKey('')
      toast.success(t('adm.aip.saved'))
    },
    onError: () => toast.error(t('adm.aip.err_save')),
  })

  const testMut = useAppMutation({
    mutationFn: () => testLLMCompletion(testPrompt, draft?.model || undefined),
    onSuccess: (r) => {
      // Model ids usually already carry their provider prefix (e.g.
      // "anthropic/claude-haiku-4-5"), so only prepend the provider when it
      // doesn't — otherwise the line reads "anthropic/anthropic/claude-…".
      const modelRef = r.model.includes('/') ? r.model : `${r.provider}/${r.model}`
      setTestResult(`${r.content}\n\n— ${modelRef} · ${r.elapsed_ms}ms · ${r.input_tokens + r.output_tokens} tok${r.fallback_used ? ' (fallback)' : ''}`)
      toast.success(t('adm.aip.test_ok'))
    },
    onError: (err: { response?: { status?: number; data?: { detail?: string } } }) => {
      const detail = err.response?.data?.detail || t('adm.aip.err_test')
      setTestResult(`ERROR: ${detail}`)
      toast.error(detail)
    },
  })

  if (isError) {
    return <ErrorState message={t('adm.aip.err_load')} onRetry={() => void refetch()} />
  }
  if (isLoading || !draft) {
    return (
      <div className="flex items-center justify-center py-12">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  const showExportControlWarning = !US_PROVIDERS.has(draft.provider) && draft.provider !== 'custom'

  const handleSave = () => {
    // base_url contract: empty string clears (overwrites DB); undefined
    // preserves. The earlier `|| undefined` made clearing impossible —
    // a blank field was sent as undefined and the backend kept the old
    // value, which is how a stale email-shaped paste (BUG-01) stayed
    // pinned even after the user wiped the input.
    const patch: TenantLLMConfigPatch = {
      provider: draft.provider,
      model: draft.model,
      fallback_model: draft.fallback_model,
      base_url: draft.base_url ?? '',
      rate_limit_rpm: Number(draft.rate_limit_rpm),
      daily_budget_usd: Number(draft.daily_budget_usd),
      air_gapped: draft.air_gapped,
    }
    if (pendingKey) patch.api_key = pendingKey
    saveMut.mutate(patch)
  }

  const handleClearKey = () => {
    saveMut.mutate({ api_key: '' })
  }

  const modelOptions = PROVIDER_DEFAULT_MODELS[draft.provider]?.map((m) => ({ value: m, label: m })) ?? []

  return (
    <div>
      {/* Autofill honeypot. Chrome ignores autocomplete="off" and fills the
          signed-in user's email + password into the first text + password
          pair it finds — which landed the email in Base URL and the password
          in the write-only API-key field. These off-screen, tab-skipped
          decoys are the first such pair, so they absorb the autofill and the
          real fields load empty. */}
      <input
        type="text"
        name="username"
        autoComplete="username"
        tabIndex={-1}
        aria-hidden="true"
        className="pointer-events-none absolute -left-[9999px] h-px w-px opacity-0"
      />
      <input
        type="password"
        name="current-password"
        autoComplete="current-password"
        tabIndex={-1}
        aria-hidden="true"
        className="pointer-events-none absolute -left-[9999px] h-px w-px opacity-0"
      />
      <PageHeader variant="section"
        title={t('adm.aip.title')}
        description={t('adm.aip.description')}
      />

      <div className="mt-6 space-y-5">
        <div className="grid gap-3 md:grid-cols-3">
          <UsageCard label={t('adm.aip.u_calls')} value={(usage?.totals.calls ?? 0).toLocaleString()} />
          <UsageCard label={t('adm.aip.u_tokens')} value={((usage?.totals.input_tokens ?? 0) + (usage?.totals.output_tokens ?? 0)).toLocaleString()} />
          <UsageCard
            label={t('adm.aip.u_cost')}
            value={formatUsd(usage?.totals.cost_usd)}
            icon={<Coins className="h-4 w-4 text-warning" />}
          />
        </div>

        <div className="grid gap-5 xl:grid-cols-2">
          <div className="min-w-0 space-y-5">
        <Section title={t('adm.aip.s_provider')} icon={<KeyRound className="h-4 w-4" />}>
          {showExportControlWarning && (
            <div
              className="mb-3 flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs text-warning-strong dark:border-warning/40"
              data-testid="export-control-warning"
            >
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
              <div>
                <p className="font-medium">{t('adm.aip.compliance')}</p>
                <p>{t('adm.aip.compliance_desc')}</p>
              </div>
            </div>
          )}
          <Select
            label={t('adm.aip.provider')}
            value={draft.provider}
            onValueChange={(v) =>
              setDraft({ ...draft, provider: v as TenantLLMConfig['provider'] })
            }
            options={PROVIDER_OPTIONS}
          />
          {modelOptions.length > 0 ? (
            <Select
              label={t('adm.aip.primary_model')}
              value={draft.model || modelOptions[0]?.value || ''}
              onValueChange={(v) => setDraft({ ...draft, model: v })}
              options={modelOptions}
              className="mt-3"
            />
          ) : (
            <Input
              label={t('adm.aip.primary_model')}
              value={draft.model}
              onChange={(e) => setDraft({ ...draft, model: e.target.value })}
              dir="ltr"
              placeholder={t('adm.aip.primary_model_ph')}
              className="mt-3"
            />
          )}
          <Input
            label={t('adm.aip.fallback')}
            value={draft.fallback_model}
            onChange={(e) => setDraft({ ...draft, fallback_model: e.target.value })}
            dir="ltr"
            placeholder={t('adm.aip.fallback_ph')}
            className="mt-3"
          />
          <Input
            label={t('adm.aip.base_url')}
            value={draft.base_url ?? ''}
            onChange={(e) => setDraft({ ...draft, base_url: e.target.value || null })}
            dir="ltr"
            placeholder={t('adm.aip.base_url_ph')}
            type="url"
            // Stop the browser auto-filling the logged-in user's email into this
            // URL field. name is non-standard so heuristic autofill won't match.
            autoComplete="off"
            name="ai-base-url"
            data-lpignore="true"
            data-1p-ignore="true"
            data-form-type="other"
            error={
              draft.base_url && !/^https?:\/\//.test(draft.base_url)
                ? t('adm.aip.err_scheme')
                : draft.base_url && /^[^/\s]+@[^/\s]+$/.test(draft.base_url)
                  ? t('adm.aip.err_email')
                  : undefined
            }
            className="mt-3"
          />
        </Section>

        <Section title={t('adm.aip.new_key')} icon={<KeyRound className="h-4 w-4" />}>
          <p className="mb-2 text-sm text-muted-foreground">
            {draft.key_set
              ? (
                <Trans
                  i18nKey="adm.aip.key_set"
                  ns="common"
                  values={{ when: draft.key_set_at ? formatRelativeTime(draft.key_set_at) : t('adm.aip.never') }}
                  components={[<strong key="w" />]}
                />
              )
              : t('adm.aip.key_unset')}
          </p>
          <PasswordInput
            label={t('adm.aip.new_key')}
            // "new-password" (not "off", which Chrome ignores) stops the
            // browser autofilling the signed-in user's SAVED password into
            // this write-only field. The data-* hints opt out of 1Password /
            // LastPass too. See also the honeypot decoys at the top of the form.
            autoComplete="new-password"
            data-lpignore="true"
            data-1p-ignore="true"
            data-form-type="other"
            value={pendingKey}
            onChange={(e) => setPendingKey(e.target.value)}
            dir="ltr"
            placeholder={draft.key_set ? t('adm.aip.new_key_keep') : t('adm.aip.new_key_ph')}
            data-testid="llm-api-key-input"
          />
          {draft.key_set && (
            <Button
              variant="ghost"
              onClick={handleClearKey}
              disabled={saveMut.isPending}
              className="mt-2"
              data-testid="llm-api-key-clear"
            >
              {t('adm.aip.clear_key')}
            </Button>
          )}
        </Section>

          </div>
          <div className="min-w-0 space-y-5">
        <Section title={t('adm.aip.s_limits')} icon={<ShieldAlert className="h-4 w-4" />}>
          <div className="grid gap-3 md:grid-cols-2">
            <Input
              label={t('adm.aip.rate_limit')}
              type="number"
              min={0}
              value={draft.rate_limit_rpm}
              onChange={(e) => setDraft({ ...draft, rate_limit_rpm: Number(e.target.value) })}
              data-testid="llm-rate-limit"
            />
            <Input
              label={t('adm.aip.daily_budget')}
              type="number"
              min={0}
              step="0.01"
              value={draft.daily_budget_usd}
              onChange={(e) => setDraft({ ...draft, daily_budget_usd: Number(e.target.value) })}
              data-testid="llm-daily-budget"
            />
          </div>
          <label className="mt-3 flex cursor-pointer items-center justify-between rounded-md border border-border px-3 py-2">
            <div>
              <div className="text-sm font-medium">{t('adm.aip.air_gapped')}</div>
              <div className="text-xs text-muted-foreground">
                {t('adm.aip.air_gapped_desc')}
              </div>
            </div>
            <input
              type="checkbox"
              checked={draft.air_gapped}
              onChange={(e) => setDraft({ ...draft, air_gapped: e.target.checked })}
              data-testid="llm-air-gapped"
            />
          </label>
        </Section>

        <Section title={t('adm.aip.s_test')} icon={<Beaker className="h-4 w-4" />}>
          <Input
            label={t('adm.aip.prompt')}
            value={testPrompt}
            onChange={(e) => setTestPrompt(e.target.value)}
            data-testid="llm-test-prompt"
          />
          <Button
            variant="ghost"
            onClick={() => testMut.mutate()}
            disabled={testMut.isPending}
            className="mt-2"
            data-testid="llm-test-run"
          >
            {testMut.isPending ? <Spinner className="h-4 w-4" /> : <Beaker className="h-4 w-4" />}
            {t('adm.aip.run_test')}
          </Button>
          {testResult && (
            <pre
              className="mt-3 max-h-40 overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted p-3 text-xs"
              data-testid="llm-test-result"
            >
              {testResult}
            </pre>
          )}
        </Section>
          </div>
        </div>

        <div className="flex justify-end gap-2">
          <Button
            onClick={handleSave}
            disabled={saveMut.isPending}
            data-testid="llm-save"
          >
            {saveMut.isPending ? <Spinner className="h-4 w-4" /> : null}
            {t('adm.aip.save')}
          </Button>
        </div>
      </div>
    </div>
  )
}

function Section({ title, icon, children }: {
  title: string
  icon?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="rounded-lg border border-border bg-card p-4">
      <h3 className="mb-3 flex items-center gap-2 text-sm font-medium">
        {icon}
        {title}
      </h3>
      {children}
    </section>
  )
}

function UsageCard({ label, value, icon }: {
  label: string
  value: string
  icon?: React.ReactNode
}) {
  return (
    <div className="rounded-lg border border-border bg-card p-3">
      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>{label}</span>
        {icon}
      </div>
      <div className="mt-1 text-xl font-semibold">{value}</div>
    </div>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/ai?tab=provider). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/tenant/ai')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/ai', search: { tab: 'provider' }, replace: true })
  },
})
