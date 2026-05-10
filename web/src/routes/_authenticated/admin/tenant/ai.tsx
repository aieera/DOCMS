import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
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

const PROVIDER_OPTIONS = [
  { value: 'anthropic',  label: 'Anthropic (Claude)' },
  { value: 'openai',     label: 'OpenAI (GPT)' },
  { value: 'bedrock',    label: 'AWS Bedrock' },
  { value: 'vllm_local', label: 'vLLM (self-hosted, on-prem)' },
  { value: 'custom',     label: 'Custom (advanced)' },
]

// US-domiciled providers that don't trigger the export-control banner.
// vLLM is the on-prem path; bedrock is AWS US (regional config is the
// admin's responsibility and out of scope here).
const US_PROVIDERS = new Set(['vllm_local', 'openai', 'bedrock'])

const PROVIDER_DEFAULT_MODELS: Record<string, string[]> = {
  anthropic:  ['anthropic/claude-haiku-4-5', 'anthropic/claude-sonnet-4-6', 'anthropic/claude-opus-4-7'],
  openai:     ['openai/gpt-4o-mini', 'openai/gpt-4o', 'openai/gpt-4-turbo'],
  bedrock:    ['bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0'],
  vllm_local: ['vllm/meta-llama/Llama-3.1-70B-Instruct', 'vllm/mistralai/Mistral-Large-Instruct-2407'],
  custom:     [],
}

function relativeTime(iso: string | null): string {
  if (!iso) return 'never'
  const ms = Date.now() - new Date(iso).getTime()
  if (ms < 60_000) return 'just now'
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)} min ago`
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)} hr ago`
  return `${Math.floor(ms / 86_400_000)} d ago`
}

function TenantAIPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
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
  const [testPrompt, setTestPrompt] = useState<string>('Say"ack" if you can hear me.')
  const [testResult, setTestResult] = useState<string>('')

  useEffect(() => {
    if (data) setDraft(data)
  }, [data])

  const saveMut = useMutation({
    mutationFn: (patch: TenantLLMConfigPatch) => updateTenantLLMConfig(patch),
    onSuccess: (next) => {
      qc.setQueryData(['tenant-llm-config'], next)
      setPendingKey('')
      toast.success('Saved')
    },
    onError: () => toast.error('Could not save settings'),
  })

  const testMut = useMutation({
    mutationFn: () => testLLMCompletion(testPrompt, draft?.model || undefined),
    onSuccess: (r) => {
      setTestResult(`${r.content}\n\n— ${r.provider}/${r.model} · ${r.elapsed_ms}ms · ${r.input_tokens + r.output_tokens} tok${r.fallback_used ? ' (fallback)' : ''}`)
      toast.success('Test call succeeded')
    },
    onError: (err: { response?: { status?: number; data?: { detail?: string } } }) => {
      const detail = err.response?.data?.detail || 'Test call failed'
      setTestResult(`ERROR: ${detail}`)
      toast.error(detail)
    },
  })

  if (isLoading || !draft) {
    return (
      <div className="flex items-center justify-center py-12">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  const showExportControlWarning = !US_PROVIDERS.has(draft.provider) && draft.provider !== 'custom'

  const handleSave = () => {
    const patch: TenantLLMConfigPatch = {
      provider: draft.provider,
      model: draft.model,
      fallback_model: draft.fallback_model,
      base_url: draft.base_url || undefined,
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
    <div className="mx-auto max-w-3xl">
      <PageHeader
        title="AI provider"
        description="Per-tenant LLM routing — provider, model, fallback, encrypted API key, rate limit, daily budget. ADR 0081."
      />

      <div className="mt-6 space-y-5">
        <div className="grid gap-3 md:grid-cols-3">
          <UsageCard label="Calls" value={(usage?.totals.calls ?? 0).toLocaleString()} />
          <UsageCard label="Total tokens" value={((usage?.totals.input_tokens ?? 0) + (usage?.totals.output_tokens ?? 0)).toLocaleString()} />
          <UsageCard
            label="Cost"
            value={`$${(usage?.totals.cost_usd ?? 0).toFixed(4)}`}
            icon={<Coins className="h-4 w-4 text-warning" />}
          />
        </div>

        <Section title="Provider" icon={<KeyRound className="h-4 w-4" />}>
          {showExportControlWarning && (
            <div
              className="mb-3 flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs text-warning dark:border-warning/40"
              data-testid="export-control-warning"
            >
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
              <div>
                <p className="font-medium">Compliance check</p>
                <p>Selected provider is not US-domiciled. Confirm export-control + data-residency requirements before saving.</p>
              </div>
            </div>
          )}
          <Select
            label="Provider"
            value={draft.provider}
            onValueChange={(v) =>
              setDraft({ ...draft, provider: v as TenantLLMConfig['provider'] })
            }
            options={PROVIDER_OPTIONS}
          />
          {modelOptions.length > 0 ? (
            <Select
              label="Primary model"
              value={draft.model || modelOptions[0]?.value || ''}
              onValueChange={(v) => setDraft({ ...draft, model: v })}
              options={modelOptions}
              className="mt-3"
            />
          ) : (
            <Input
              label="Primary model"
              value={draft.model}
              onChange={(e) => setDraft({ ...draft, model: e.target.value })}
              placeholder="e.g. provider/model-id"
              className="mt-3"
            />
          )}
          <Input
            label="Fallback model (empty disables fallback)"
            value={draft.fallback_model}
            onChange={(e) => setDraft({ ...draft, fallback_model: e.target.value })}
            placeholder="e.g. anthropic/claude-haiku-4-5"
            className="mt-3"
          />
          <Input
            label="Base URL (optional, for self-hosted endpoints)"
            value={draft.base_url ?? ''}
            onChange={(e) => setDraft({ ...draft, base_url: e.target.value || null })}
            placeholder="https://your-vllm.local/v1"
            className="mt-3"
          />
        </Section>

        <Section title="API key" icon={<KeyRound className="h-4 w-4" />}>
          <p className="mb-2 text-sm text-muted-foreground">
            {draft.key_set
              ? <>Key set <strong>{relativeTime(draft.key_set_at)}</strong>. Stored AES-256-GCM encrypted at rest. Never returned in plaintext after save.</>
              : <>No key set. Without a key, calls fall back to the deploy-default credentials.</>}
          </p>
          <Input
            label="New API key (write-only)"
            type="password"
            autoComplete="off"
            value={pendingKey}
            onChange={(e) => setPendingKey(e.target.value)}
            placeholder={draft.key_set ? 'Leave blank to keep existing' : 'sk-...'}
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
              Clear stored key
            </Button>
          )}
        </Section>

        <Section title="Limits" icon={<ShieldAlert className="h-4 w-4" />}>
          <div className="grid gap-3 md:grid-cols-2">
            <Input
              label="Rate limit (requests / min)"
              type="number"
              min={0}
              value={draft.rate_limit_rpm}
              onChange={(e) => setDraft({ ...draft, rate_limit_rpm: Number(e.target.value) })}
              data-testid="llm-rate-limit"
            />
            <Input
              label="Daily budget (USD; 0 disables)"
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
              <div className="text-sm font-medium">Air-gapped (this tenant only)</div>
              <div className="text-xs text-muted-foreground">
                Force vLLM-only routing. The gateway rejects any external-provider call regardless of model id. Stacks with the deploy-time VAULTDMS_AIR_GAPPED env.
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

        <div className="flex justify-end gap-2">
          <Button
            onClick={handleSave}
            disabled={saveMut.isPending}
            data-testid="llm-save"
          >
            {saveMut.isPending ? <Spinner className="h-4 w-4" /> : null}
            Save
          </Button>
        </div>

        <Section title="Test the configuration" icon={<Beaker className="h-4 w-4" />}>
          <Input
            label="Prompt"
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
            Run test
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

export const Route = createFileRoute('/_authenticated/admin/tenant/ai')({
  component: TenantAIPage,
})
