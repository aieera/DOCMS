import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Plus, Trash2, AlertTriangle } from 'lucide-react'

import {
  createRoutingRule,
  deleteRoutingRule,
  getSmartRoutingConfig,
  listRoutingRules,
  updateRoutingRule,
  updateSmartRoutingConfig,
  type RoutingRule,
  type SmartRoutingConfig,
} from '@/api/smart-routing'
import { useAuthStore } from '@/store/authStore'
import { IntelligenceConfigCard, type ConfigField } from '@/components/admin/IntelligenceConfigCard'
import { getWorkspaces, getFolders } from '@/api/workspaces'
import type { Folder, Workspace } from '@/types/api'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'

interface CreateForm {
  name: string
  category_key: string
  target_folder_id: string
  // Held as the raw string while editing so intermediate states ("-" on the
  // way to "-5") aren't coerced to 0 — which dropped the sign (BUG-07).
  // Parsed to an int on submit; the backend column is a signed int32.
  priority: string
}

const EMPTY: CreateForm = { name: '', category_key: '', target_folder_id: '', priority: '0' }

// GET/PUT /admin/smart-routing-config — endpoints that existed (and the
// hub card promised) but had no UI until now.
const ROUTING_CONFIG_FIELDS: ConfigField<SmartRoutingConfig>[] = [
  { key: 'enabled', label: 'Smart routing enabled', kind: 'toggle', hint: 'Master switch for suggestions and auto-moves' },
  { key: 'auto_move_threshold', label: 'Auto-move threshold', kind: 'number', min: 0, max: 1, step: 0.05, hint: 'Confidence at which a document is filed automatically' },
  { key: 'suggest_threshold', label: 'Suggest threshold', kind: 'number', min: 0, max: 1, step: 0.05, hint: 'Confidence at which a filing suggestion is shown' },
  { key: 'max_suggestions', label: 'Max suggestions', kind: 'number', min: 1, max: 10, step: 1 },
  { key: 'learn_from_history', label: 'Learn from filing history', kind: 'toggle' },
  { key: 'use_similarity', label: 'Use content similarity', kind: 'toggle' },
]

function RoutingRulesPage() {
  const qc = useQueryClient()
  const role = useAuthStore((st) => st.user?.role)
  const canEditConfig = role === 'admin' || role === 'owner'
  const [creating, setCreating] = useState<CreateForm | null>(null)

  const { data: rules, isLoading } = useQuery({
    queryKey: ['routing-rules'],
    queryFn: listRoutingRules,
  })

  // Resolve target_folder_id → a human folder name for the table. Rules only
  // store the UUID (the list endpoint doesn't join folders), so we load
  // folders and build an id→path map; unresolved ids fall back to a short,
  // copy-friendly UUID rather than the bare raw value (BUG-08).
  const workspacesQ = useQuery({
    queryKey: ['routing-rules', 'workspaces'],
    queryFn: getWorkspaces,
    staleTime: 60_000,
  })
  // Only fetch folders for workspaces a rule actually targets — this
  // used to fan out one request per workspace in the tenant on every
  // page load just to label the table. Rules without a
  // target_workspace_id (legacy rows) fall back to the full sweep.
  const targetWorkspaceIds = new Set(
    (rules ?? []).map((r) => r.target_workspace_id).filter((id): id is string => !!id),
  )
  const needFullSweep = (rules ?? []).some((r) => !r.target_workspace_id)
  const workspacesToFetch = needFullSweep
    ? (workspacesQ.data ?? [])
    : (workspacesQ.data ?? []).filter((w: Workspace) => targetWorkspaceIds.has(w.id))
  const folderQueries = useQueries({
    queries: workspacesToFetch.map((w: Workspace) => ({
      queryKey: ['routing-rules', 'folders', w.id],
      queryFn: () => getFolders(w.id),
      staleTime: 60_000,
    })),
  })
  const folderNameById = new Map<string, string>()
  for (const q of folderQueries) {
    for (const f of (q.data as Folder[] | undefined) ?? []) {
      folderNameById.set(f.id, f.path || f.name)
    }
  }

  const create = useAppMutation({
    mutationFn: createRoutingRule,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['routing-rules'] })
      setCreating(null)
      toast.success('Rule created')
    },
    onError: () => toast.error('Create failed'),
  })

  const toggle = useAppMutation({
    mutationFn: (rule: RoutingRule) =>
      updateRoutingRule(rule.id, { enabled: !rule.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['routing-rules'] }),
    defaultErrorMessage: 'Could not toggle the rule',
  })

  const remove = useAppMutation({
    mutationFn: deleteRoutingRule,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['routing-rules'] })
      toast.success('Rule deleted')
    },
    onError: () => toast.error('Delete failed'),
  })

  const submit = () => {
    if (!creating) return
    if (!creating.name || !creating.category_key || !creating.target_folder_id) {
      toast.error('Name, category, and target folder are required')
      return
    }
    // parseInt keeps the sign ("-5" → -5); empty / non-numeric → 0.
    const priority = parseInt(creating.priority, 10)
    create.mutate({
      name: creating.name,
      category_key: creating.category_key,
      target_folder_id: creating.target_folder_id,
      priority: Number.isNaN(priority) ? 0 : priority,
    })
  }

  return (
    <div className="max-w-5xl">
      <PageHeader
        title="Routing rules"
        description="When a document is classified, matching rules emit folder suggestions."
        actions={
          !creating && (
            <Button size="sm" onClick={() => setCreating({ ...EMPTY })}>
              <Plus className="me-2 h-4 w-4" />
              New rule
            </Button>
          )
        }
      />

      {canEditConfig && (
        <div className="mt-6">
          <IntelligenceConfigCard
            title="Configuration"
            description="Thresholds and behavior for smart filing across the tenant."
            queryKey={['smart-routing-config']}
            fetchConfig={getSmartRoutingConfig}
            saveConfig={updateSmartRoutingConfig}
            fields={ROUTING_CONFIG_FIELDS}
            testid="smart-routing-config"
          />
        </div>
      )}

      {creating && (
        <div className="mt-4 rounded border border-border p-4">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <LabeledInput label="Name">
              <Input
                value={creating.name}
                onChange={(e) => setCreating({ ...creating, name: e.target.value })}
                placeholder="Invoices → Finance"
              />
            </LabeledInput>
            <LabeledInput label="Category key">
              <Input
                value={creating.category_key}
                onChange={(e) => setCreating({ ...creating, category_key: e.target.value })}
                placeholder="invoice"
              />
            </LabeledInput>
            <LabeledInput label="Target folder">
              <FolderPicker
                value={creating.target_folder_id}
                onChange={(id) => setCreating({ ...creating, target_folder_id: id })}
              />
            </LabeledInput>
            <LabeledInput label="Priority">
              <Input
                type="number"
                value={creating.priority}
                onChange={(e) => setCreating({ ...creating, priority: e.target.value })}
                placeholder="0"
              />
            </LabeledInput>
          </div>
          <div className="mt-4 flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => setCreating(null)}>
              Cancel
            </Button>
            <Button size="sm" disabled={create.isPending} onClick={submit}>
              Create
            </Button>
          </div>
        </div>
      )}

      <div className="mt-6 overflow-x-auto rounded border border-border">
        <table className="w-full text-sm">
          <thead className="bg-muted/40 text-start text-xs uppercase tracking-wide text-muted-foreground">
            <tr>
              <th className="px-4 py-2">Name</th>
              <th className="px-4 py-2">Category</th>
              <th className="px-4 py-2">Target folder</th>
              <th className="px-4 py-2">Priority</th>
              <th className="px-4 py-2">Status</th>
              <th className="px-4 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {isLoading && (
              <tr>
                <td colSpan={6} className="p-4 text-center text-muted-foreground">
                  Loading…
                </td>
              </tr>
            )}
            {!isLoading && (rules ?? []).length === 0 && (
              <tr>
                <td colSpan={6} className="p-4 text-center text-muted-foreground">
                  No routing rules yet.
                </td>
              </tr>
            )}
            {(rules ?? []).map((r) => (
              <tr key={r.id} className="border-t border-border">
                <td className="px-4 py-2 font-medium">{r.name}</td>
                <td className="px-4 py-2">
                  <Badge variant="outline">{r.category_key}</Badge>
                </td>
                <td className="px-4 py-2">
                  {folderNameById.get(r.target_folder_id) ? (
                    <span className="font-medium">{folderNameById.get(r.target_folder_id)}</span>
                  ) : (
                    <span className="font-mono text-xs text-muted-foreground" title={r.target_folder_id}>
                      {r.target_folder_id.slice(0, 8)}…
                    </span>
                  )}
                </td>
                <td className="px-4 py-2 tabular-nums">{r.priority}</td>
                <td className="px-4 py-2">
                  <button
                    onClick={() => toggle.mutate(r)}
                    className="cursor-pointer"
                    aria-label={r.enabled ? 'Disable' : 'Enable'}
                  >
                    <Badge variant={r.enabled ? 'active' : 'archived'}>
                      {r.enabled ? 'enabled' : 'disabled'}
                    </Badge>
                  </button>
                </td>
                <td className="px-4 py-2 text-end">
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label="Delete"
                    disabled={remove.isPending}
                    onClick={() => {
                      if (window.confirm(`Delete rule"${r.name}"?`)) remove.mutate(r.id)
                    }}
                  >
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function LabeledInput({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="text-sm">
      <span className="mb-1 block text-muted-foreground">{label}</span>
      {children}
    </label>
  )
}

// FolderPicker — two-step picker replacing the raw-UUID input
// (BUG-19). Step 1: pick a workspace. Step 2: pick a folder inside
// it. The value flowing into the form is still the folder UUID
// the backend expects.
function FolderPicker({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const [workspaceId, setWorkspaceId] = useState<string>('')

  const wsQ = useQuery({
    queryKey: ['admin', 'folder-picker', 'workspaces'],
    queryFn: getWorkspaces,
    staleTime: 60_000,
  })
  const foldersQ = useQuery({
    queryKey: ['admin', 'folder-picker', 'folders', workspaceId],
    queryFn: () => getFolders(workspaceId),
    enabled: !!workspaceId,
    staleTime: 30_000,
  })

  // If the caller already has a folder id, work backwards to set the
  // workspace selector so the dropdown reads sensibly on edit.
  useEffect(() => {
    if (!value || workspaceId) return
    const f = (foldersQ.data ?? []).find((x) => x.id === value)
    if (f) setWorkspaceId(f.workspace_id)
  }, [value, workspaceId, foldersQ.data])

  return (
    <div className="grid gap-2 sm:grid-cols-2">
      <div className="space-y-1">
        <select
          value={workspaceId}
          onChange={(e) => { setWorkspaceId(e.target.value); onChange('') }}
          className="h-9 w-full rounded-md border border-border bg-background px-2 text-sm"
          data-testid="folder-picker-workspace"
        >
          <option value="">— pick workspace —</option>
          {(wsQ.data ?? []).map((w: Workspace) => (
            <option key={w.id} value={w.id}>{w.name}</option>
          ))}
        </select>
        {/* Wave 5 pattern 3: getWorkspaces/getFolders throw on a
            malformed response (was silently returning []). Surface the
            error instead of an empty dropdown that reads as "no
            workspaces / folders" — cf. VersionHistory. */}
        {wsQ.isError && (
          <p className="flex items-center gap-1.5 text-xs text-destructive" role="alert">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden />
            Couldn&apos;t load workspaces
          </p>
        )}
      </div>
      <div className="space-y-1">
        <select
          value={value}
          onChange={(e) => onChange(e.target.value)}
          disabled={!workspaceId || foldersQ.isLoading}
          className="h-9 w-full rounded-md border border-border bg-background px-2 text-sm disabled:opacity-50"
          data-testid="folder-picker-folder"
        >
          <option value="">{workspaceId ? '— pick folder —' : 'Pick a workspace first'}</option>
          {(foldersQ.data ?? []).map((f: Folder) => (
            <option key={f.id} value={f.id}>{f.path || f.name}</option>
          ))}
        </select>
        {foldersQ.isError && (
          <p className="flex items-center gap-1.5 text-xs text-destructive" role="alert">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden />
            Couldn&apos;t load folders
          </p>
        )}
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/routing-rules')({
  component: RoutingRulesPage,
})
