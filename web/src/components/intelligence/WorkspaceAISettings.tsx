import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'

import {
  getWorkspaceAISettings,
  updateWorkspaceAISettings,
  type WorkspaceAISettings,
} from '@/api/rag'
import { Dialog } from '@/components/ui/Dialog'
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
]

const EMBEDDING_MODEL_OPTIONS = [
  { value: 'bge-large-en-v1.5', label: 'BGE Large EN v1.5 (default)' },
  { value: 'bge-m3',            label: 'BGE-M3 (multilingual)' },
]

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
}

export function WorkspaceAISettingsDialog({ open, onOpenChange, workspaceId }: Props) {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['workspace-ai-settings', workspaceId],
    queryFn: () => getWorkspaceAISettings(workspaceId),
    enabled: open,
  })

  const [draft, setDraft] = useState<WorkspaceAISettings | null>(null)
  // Sync draft from server data each time the dialog opens.
  useEffect(() => {
    if (data) setDraft(data)
  }, [data])

  const saveMut = useMutation({
    mutationFn: (patch: Partial<WorkspaceAISettings>) =>
      updateWorkspaceAISettings(workspaceId, patch),
    onSuccess: (next) => {
      qc.setQueryData(['workspace-ai-settings', workspaceId], next)
      toast.success('AI settings saved')
      onOpenChange(false)
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

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="AI settings"
      description="Per-workspace controls for the Ask page (RAG)."
     
    >
      {isLoading || !draft ? (
        <div className="flex items-center justify-center py-8">
          <Spinner className="h-5 w-5" />
        </div>
      ) : (
        <div className="space-y-4" data-testid="ai-settings-form">
          <label className="flex cursor-pointer items-center justify-between rounded-md border border-[var(--color-border)] px-3 py-2">
            <div>
              <div className="text-sm font-medium">Enable Ask in this workspace</div>
              <div className="text-xs text-[var(--color-text-secondary)]">
                When off, /rag/query rejects with 403 for this workspace.
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
            onChange={(e) =>
              setDraft({ ...draft, rag_queries_per_day: Number(e.target.value) })
            }
            data-testid="ai-settings-quota"
          />
          <p className="-mt-2 text-xs text-[var(--color-text-secondary)]">
            0 disables the gate. Each /rag/query call counts against the rolling 24h window.
          </p>

          <div className="flex justify-end gap-2 pt-2">
            <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button
              onClick={handleSave}
              disabled={saveMut.isPending}
              data-testid="ai-settings-save"
            >
              {saveMut.isPending ? <Spinner className="h-4 w-4" /> : null}
              Save
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  )
}
