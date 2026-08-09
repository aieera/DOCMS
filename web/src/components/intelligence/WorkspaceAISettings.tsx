import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'

import {
  getWorkspaceAISettings,
  updateWorkspaceAISettings,
  type WorkspaceAISettings,
} from '@/api/rag'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Spinner } from '@/components/ui/Spinner'

// Curated allow-list of LLMs the AI surface supports. Anything outside
// this list still works (saved as free text via the API), but the
// dropdown keeps admins from typo'ing model ids.
const ANSWER_MODEL_OPTIONS = [
  { value: 'anthropic/claude-haiku-4-5',  label: 'Claude Haiku 4.5 (fast, cheap)' },
  { value: 'anthropic/claude-sonnet-4-6', label: 'Claude Sonnet 4.6 (balanced)' },
  { value: 'anthropic/claude-opus-4-7',   label: 'Claude Opus 4.7 (best)' },
  { value: 'openai/gpt-4o-mini',          label: 'GPT-4o mini (OpenAI)' },
  { value: 'openai/gpt-4o',               label: 'GPT-4o (OpenAI)' },
  { value: 'gemini/gemini-2.5-flash',     label: 'Gemini 2.5 Flash (Google, fast)' },
  { value: 'gemini/gemini-2.5-pro',       label: 'Gemini 2.5 Pro (Google, best)' },
]

const EMBEDDING_MODEL_OPTIONS = [
  { value: 'bge-large-en-v1.5', label: 'BGE Large EN v1.5 (default)' },
  { value: 'bge-m3',            label: 'BGE-M3 (multilingual)' },
]

/**
 * Per-workspace AI (Ask/RAG) controls, rendered as an inline section.
 * Lives inside the Workspace Settings modal (no longer a standalone
 * dialog) — see WorkspaceSettingsDialog. The answer model selects which
 * LLM the Ask page uses; the tenant LLM config (admin/tenant/ai) supplies
 * the matching provider key.
 */
export function WorkspaceAISettingsSection({ workspaceId }: { workspaceId: string }) {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['workspace-ai-settings', workspaceId],
    queryFn: () => getWorkspaceAISettings(workspaceId),
  })

  const [draft, setDraft] = useState<WorkspaceAISettings | null>(null)
  useEffect(() => { if (data) setDraft(data) }, [data])

  const saveMut = useAppMutation({
    mutationFn: (patch: Partial<WorkspaceAISettings>) =>
      updateWorkspaceAISettings(workspaceId, patch),
    onSuccess: (next) => {
      qc.setQueryData(['workspace-ai-settings', workspaceId], next)
      toast.success('AI settings saved')
    },
    onError: () => toast.error('Could not save settings'),
  })

  const handleSave = () => {
    if (!draft) return
    saveMut.mutate({
      rag_enabled: draft.rag_enabled,
      answer_model: draft.answer_model,
      embedding_model: draft.embedding_model,
      rag_queries_per_day: Number(draft.rag_queries_per_day),
    })
  }

  if (isLoading || !draft) {
    return (
      <div className="flex items-center justify-center py-8">
        <Spinner className="h-5 w-5" />
      </div>
    )
  }

  return (
    <div className="space-y-4" data-testid="ai-settings-form">
      <label className="flex cursor-pointer items-center justify-between rounded-md border border-border px-3 py-2">
        <div>
          <div className="text-sm font-medium">Enable Ask in this workspace</div>
          <div className="text-xs text-muted-foreground">
            When off, this workspace's documents can't be used to answer questions on the Ask page.
          </div>
        </div>
        <input
          type="checkbox"
          checked={draft.rag_enabled}
          onChange={(e) => setDraft({ ...draft, rag_enabled: e.target.checked })}
          data-testid="ai-settings-enabled"
        />
      </label>

      <Select
        label="Answer model"
        value={draft.answer_model}
        onValueChange={(v) => setDraft({ ...draft, answer_model: v })}
        options={ANSWER_MODEL_OPTIONS}
      />
      <p className="-mt-2 text-xs text-muted-foreground">
        Pick a model whose provider key is configured under{' '}
        <a className="underline" href="/admin/tenant/ai">Tenant → AI</a> (e.g. a
        Gemini model needs a Gemini key there).
      </p>

      <Select
        label="Embedding model"
        value={draft.embedding_model}
        onValueChange={(v) => setDraft({ ...draft, embedding_model: v })}
        options={EMBEDDING_MODEL_OPTIONS}
      />

      <Input
        label="Per-user daily query limit"
        type="number"
        min={0}
        value={draft.rag_queries_per_day}
        onChange={(e) => setDraft({ ...draft, rag_queries_per_day: Number(e.target.value) })}
        data-testid="ai-settings-quota"
      />
      <p className="-mt-2 text-xs text-muted-foreground">
        0 disables the gate. Each Ask query counts against the rolling 24h window.
      </p>

      <div className="flex justify-end">
        <Button
          onClick={handleSave}
          disabled={saveMut.isPending}
          loading={saveMut.isPending}
          data-testid="ai-settings-save"
        >
          Save AI settings
        </Button>
      </div>
    </div>
  )
}
