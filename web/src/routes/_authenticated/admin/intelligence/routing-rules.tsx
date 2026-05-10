import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Plus, Trash2 } from 'lucide-react'

import {
  createRoutingRule,
  deleteRoutingRule,
  listRoutingRules,
  updateRoutingRule,
  type RoutingRule,
} from '@/api/smart-routing'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'

interface CreateForm {
  name: string
  category_key: string
  target_folder_id: string
  priority: number
}

const EMPTY: CreateForm = { name: '', category_key: '', target_folder_id: '', priority: 0 }

function RoutingRulesPage() {
  const qc = useQueryClient()
  const [creating, setCreating] = useState<CreateForm | null>(null)

  const { data: rules, isLoading } = useQuery({
    queryKey: ['routing-rules'],
    queryFn: listRoutingRules,
  })

  const create = useMutation({
    mutationFn: createRoutingRule,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['routing-rules'] })
      setCreating(null)
      toast.success('Rule created')
    },
    onError: () => toast.error('Create failed'),
  })

  const toggle = useMutation({
    mutationFn: (rule: RoutingRule) =>
      updateRoutingRule(rule.id, { enabled: !rule.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['routing-rules'] }),
  })

  const remove = useMutation({
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
    create.mutate({
      name: creating.name,
      category_key: creating.category_key,
      target_folder_id: creating.target_folder_id,
      priority: creating.priority,
    })
  }

  return (
    <div className="mx-auto max-w-5xl p-6">
      <PageHeader
        title="Routing rules"
        description="When a document is classified, matching rules emit folder suggestions (ADR 0053)."
        actions={
          !creating && (
            <Button size="sm" onClick={() => setCreating({ ...EMPTY })}>
              <Plus className="mr-2 h-4 w-4" />
              New rule
            </Button>
          )
        }
      />

      {creating && (
        <div className="mt-4 rounded border border-border p-4">
          <div className="grid grid-cols-2 gap-3">
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
            <LabeledInput label="Target folder ID">
              <Input
                value={creating.target_folder_id}
                onChange={(e) => setCreating({ ...creating, target_folder_id: e.target.value })}
                placeholder="UUID — folder picker comes later"
              />
            </LabeledInput>
            <LabeledInput label="Priority">
              <Input
                type="number"
                value={creating.priority}
                onChange={(e) =>
                  setCreating({ ...creating, priority: Number(e.target.value) || 0 })
                }
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

      <div className="mt-6 rounded border border-border">
        <table className="w-full text-sm">
          <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
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
                <td className="px-4 py-2 truncate font-mono text-xs">{r.target_folder_id}</td>
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
                <td className="px-4 py-2 text-right">
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

export const Route = createFileRoute('/_authenticated/admin/intelligence/routing-rules')({
  component: RoutingRulesPage,
})
