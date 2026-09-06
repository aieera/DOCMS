// Workspace templates (ADR 0118): gallery + structured tree editor +
// "new from template" provision dialog with variable prompts.
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import {
  FolderTree, Plus, Trash2, Edit, Rocket, FileText, ChevronDown, X,
} from 'lucide-react'

import {
  listTemplates, createTemplate, updateTemplate, deleteTemplate, provisionTemplate,
  type WorkspaceTemplate, type TemplateDefinition, type TemplateNode,
} from '@/api/templates'
import { api } from '@/api/client'
import { useWorkspaces } from '@/hooks/useWorkspaces'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/shadcn/input'
import { Badge } from '@/components/ui/shadcn/badge'
import { Skeleton } from '@/components/ui/Skeleton'
import { EmptyState } from '@/components/ui/EmptyState'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

export const Route = createFileRoute('/_authenticated/templates')({
  component: TemplatesPage,
})

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

function countTree(nodes: TemplateNode[]): { folders: number; docs: number } {
  let folders = 0
  let docs = 0
  const walk = (n: TemplateNode) => {
    folders++
    docs += n.docs?.length ?? 0
    n.children?.forEach(walk)
  }
  nodes.forEach(walk)
  return { folders, docs }
}

const VAR_RE = /\{\{\s*([a-zA-Z][a-zA-Z0-9_]*)\s*\}\}/g

function extractVariables(def: TemplateDefinition): string[] {
  const seen = new Set<string>()
  const collect = (s?: string) => {
    if (!s) return
    for (const m of s.matchAll(VAR_RE)) seen.add(m[1])
  }
  const walk = (n: TemplateNode) => {
    collect(n.name)
    Object.values(n.metadata ?? {}).forEach(collect)
    n.docs?.forEach((d) => {
      collect(d.title)
      Object.values(d.metadata ?? {}).forEach(collect)
      d.tags?.forEach(collect)
    })
    n.children?.forEach(walk)
  }
  def.nodes.forEach(walk)
  return Array.from(seen).sort()
}

// Immutable node update by index path.
function updateAt(nodes: TemplateNode[], path: number[], fn: (n: TemplateNode) => TemplateNode | null): TemplateNode[] {
  if (path.length === 0) return nodes
  const [head, ...rest] = path
  return nodes.flatMap((n, i) => {
    if (i !== head) return [n]
    if (rest.length === 0) {
      const out = fn(n)
      return out === null ? [] : [out]
    }
    return [{ ...n, children: updateAt(n.children ?? [], rest, fn) }]
  })
}

// ---------------------------------------------------------------------
// page
// ---------------------------------------------------------------------

function TemplatesPage() {
  const qc = useQueryClient()
  const { data: templates, isLoading } = useQuery({ queryKey: ['templates'], queryFn: listTemplates })
  const [editing, setEditing] = useState<WorkspaceTemplate | 'new' | null>(null)
  const [provisioning, setProvisioning] = useState<WorkspaceTemplate | null>(null)

  const refresh = () => void qc.invalidateQueries({ queryKey: ['templates'] })

  const del = useAppMutation({
    mutationFn: (id: string) => deleteTemplate(id),
    onSuccess: () => {
      toast.success('Template deleted')
      refresh()
    },
    defaultErrorMessage: 'Could not delete template',
  })

  return (
    <div className="p-6">
      <PageHeader
        title="Templates"
        description="Reusable folder structures — provision a project tree in one click"
        actions={
          <Button onClick={() => setEditing('new')} data-testid="new-template">
            <Plus className="h-4 w-4" /> New template
          </Button>
        }
      />

      {isLoading ? (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2].map((i) => <Skeleton key={i} className="h-36 rounded-lg" />)}
        </div>
      ) : (templates ?? []).length === 0 ? (
        <EmptyState
          icon={<FolderTree className="h-8 w-8" />}
          title="No templates yet"
          description="Define a project folder structure once, then provision it into any workspace."
        />
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {(templates ?? []).map((t) => {
            const counts = countTree(t.definition?.nodes ?? [])
            const vars = extractVariables(t.definition ?? { nodes: [] })
            return (
              <div key={t.id} className="flex flex-col rounded-lg border border-border bg-card p-4" data-testid={`template-card-${t.id}`}>
                <div className="mb-1 flex items-center gap-2">
                  <FolderTree className="h-4 w-4 text-primary" />
                  <span className="font-medium">{t.name}</span>
                </div>
                {t.description ? (
                  <p className="mb-2 line-clamp-2 text-sm text-muted-foreground">{t.description}</p>
                ) : null}
                <div className="mb-3 flex flex-wrap gap-1.5">
                  <Badge variant="secondary">{counts.folders} folders</Badge>
                  {counts.docs > 0 && <Badge variant="secondary">{counts.docs} docs</Badge>}
                  {vars.map((v) => <Badge key={v} variant="outline">{'{{'}{v}{'}}'}</Badge>)}
                </div>
                <div className="mt-auto flex gap-2">
                  <Button size="sm" onClick={() => setProvisioning(t)} data-testid={`use-template-${t.id}`}>
                    <Rocket className="h-4 w-4" /> Use
                  </Button>
                  <Button size="sm" variant="outline" onClick={() => setEditing(t)}>
                    <Edit className="h-4 w-4" /> Edit
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={`Delete template ${t.name}`}
                    onClick={() => { if (confirm(`Delete template "${t.name}"?`)) del.mutate(t.id) }}
                  >
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {editing && (
        <TemplateEditorDialog
          template={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); refresh() }}
        />
      )}
      {provisioning && (
        <ProvisionDialog template={provisioning} onClose={() => setProvisioning(null)} />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------
// editor
// ---------------------------------------------------------------------

function TemplateEditorDialog({
  template, onClose, onSaved,
}: {
  template: WorkspaceTemplate | null
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(template?.name ?? '')
  const [description, setDescription] = useState(template?.description ?? '')
  const [nodes, setNodes] = useState<TemplateNode[]>(
    template?.definition?.nodes ?? [{ name: 'Project {{project_name}}', children: [] }],
  )

  const save = useAppMutation({
    mutationFn: () => {
      const input = { name, description, definition: { nodes } }
      return template ? updateTemplate(template.id, input) : createTemplate(input)
    },
    onSuccess: () => { toast.success('Template saved'); onSaved() },
    defaultErrorMessage: 'Could not save template',
  })

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={template ? 'Edit template' : 'New template'} size="lg">
      <div className="space-y-3">
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} data-testid="template-name" />
        <Input label="Description" value={description} onChange={(e) => setDescription(e.target.value)} />
        <div>
          <div className="mb-1 flex items-center justify-between">
            <p className="text-xs font-medium">
              Folder tree — use {'{{variable}}'} for values prompted at provision time
            </p>
            <Button
              size="sm"
              variant="outline"
              onClick={() => setNodes((ns) => [...ns, { name: 'New folder' }])}
              data-testid="add-root-folder"
            >
              <Plus className="h-3.5 w-3.5" /> Root folder
            </Button>
          </div>
          <div className="max-h-96 overflow-y-auto rounded-md border border-border p-2">
            {nodes.map((n, i) => (
              <NodeEditor
                key={i}
                node={n}
                path={[i]}
                onChange={(path, fn) => setNodes((ns) => updateAt(ns, path, fn))}
              />
            ))}
            {nodes.length === 0 && (
              <p className="p-2 text-xs text-muted-foreground">Add at least one root folder.</p>
            )}
          </div>
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button
            onClick={() => save.mutate()}
            disabled={save.isPending || !name.trim() || nodes.length === 0}
            data-testid="template-save"
          >
            Save
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function NodeEditor({
  node, path, onChange,
}: {
  node: TemplateNode
  path: number[]
  onChange: (path: number[], fn: (n: TemplateNode) => TemplateNode | null) => void
}) {
  const [open, setOpen] = useState(false)
  const set = (fn: (n: TemplateNode) => TemplateNode) => onChange(path, fn)

  return (
    <div className="mb-1" style={{ marginInlineStart: (path.length - 1) * 16 }}>
      <div className="flex items-center gap-1.5">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          className="text-muted-foreground"
          aria-expanded={open}
          aria-label={`${open ? 'Collapse' : 'Expand'} folder settings for ${node.name || 'folder'}`}
        >
          {open ? <ChevronDown className="h-3.5 w-3.5" /> : <DirectionalIcon name="ChevronRight" className="h-3.5 w-3.5" />}
        </button>
        <Input
          value={node.name}
          onChange={(e) => set((n) => ({ ...n, name: e.target.value }))}
          aria-label="Folder name"
          className="h-8 flex-1 text-sm"
          data-testid={`node-name-${path.join('-')}`}
        />
        <Button
          size="sm"
          variant="ghost"
          title="Add subfolder"
          onClick={() => set((n) => ({ ...n, children: [...(n.children ?? []), { name: 'New folder' }] }))}
        >
          <Plus className="h-3.5 w-3.5" />
        </Button>
        <Button size="sm" variant="ghost" title="Remove folder" onClick={() => onChange(path, () => null)}>
          <Trash2 className="h-3.5 w-3.5 text-destructive" />
        </Button>
      </div>

      {open && (
        <div className="ms-6 mt-1 space-y-2 rounded-md border border-border bg-muted/30 p-2 text-xs">
          <label className="flex items-center gap-2">
            Visibility
            <select
              value={node.visibility ?? 'shared'}
              onChange={(e) => set((n) => ({ ...n, visibility: e.target.value === 'private' ? 'private' : undefined }))}
              className="rounded border border-border bg-background px-1 py-0.5"
            >
              <option value="shared">shared (workspace)</option>
              <option value="private">private (creator + grants)</option>
            </select>
          </label>

          <KVList
            label="Metadata defaults (inherited by documents in this folder and below)"
            entries={node.metadata ?? {}}
            onChange={(metadata) => set((n) => ({ ...n, metadata: Object.keys(metadata).length ? metadata : undefined }))}
          />

          <div>
            <div className="mb-1 flex items-center justify-between">
              <span className="font-medium">Placeholder documents</span>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => set((n) => ({ ...n, docs: [...(n.docs ?? []), { title: 'New document' }] }))}
              >
                <Plus className="h-3 w-3" /> doc
              </Button>
            </div>
            {(node.docs ?? []).map((d, di) => (
              <div key={di} className="mb-1 flex items-center gap-1.5">
                <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                <Input
                  value={d.title}
                  placeholder="Title"
                  aria-label="Document title"
                  onChange={(e) => set((n) => {
                    const docs = [...(n.docs ?? [])]
                    docs[di] = { ...docs[di], title: e.target.value }
                    return { ...n, docs }
                  })}
                  className="h-7 flex-1 text-xs"
                />
                <Input
                  value={(d.tags ?? []).join(', ')}
                  placeholder="tags, comma-separated"
                  aria-label="Document tags"
                  onChange={(e) => set((n) => {
                    const docs = [...(n.docs ?? [])]
                    const tags = e.target.value.split(',').map((t) => t.trim()).filter(Boolean)
                    docs[di] = { ...docs[di], tags: tags.length ? tags : undefined }
                    return { ...n, docs }
                  })}
                  className="h-7 w-40 text-xs"
                />
                <button
                  type="button"
                  aria-label="Remove document"
                  onClick={() => set((n) => ({ ...n, docs: (n.docs ?? []).filter((_, i) => i !== di) }))}
                >
                  <X className="h-3.5 w-3.5 text-destructive" />
                </button>
              </div>
            ))}
          </div>

          <div>
            <div className="mb-1 flex items-center justify-between">
              <span className="font-medium">Access grants (private folders)</span>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => set((n) => ({ ...n, grants: [...(n.grants ?? []), { grantee_type: 'group', grantee_id: '' }] }))}
              >
                <Plus className="h-3 w-3" /> grant
              </Button>
            </div>
            {(node.grants ?? []).map((g, gi) => (
              <div key={gi} className="mb-1 flex items-center gap-1.5">
                <select
                  aria-label="Grantee type"
                  value={g.grantee_type}
                  onChange={(e) => set((n) => {
                    const grants = [...(n.grants ?? [])]
                    grants[gi] = { ...grants[gi], grantee_type: e.target.value as 'user' | 'group' }
                    return { ...n, grants }
                  })}
                  className="rounded border border-border bg-background px-1 py-0.5"
                >
                  <option value="group">group</option>
                  <option value="user">user</option>
                </select>
                <Input
                  value={g.grantee_id}
                  placeholder="grantee UUID"
                  aria-label="Grantee UUID"
                  onChange={(e) => set((n) => {
                    const grants = [...(n.grants ?? [])]
                    grants[gi] = { ...grants[gi], grantee_id: e.target.value }
                    return { ...n, grants }
                  })}
                  className="h-7 flex-1 font-mono text-xs"
                />
                <button
                  type="button"
                  aria-label="Remove grant"
                  onClick={() => set((n) => ({ ...n, grants: (n.grants ?? []).filter((_, i) => i !== gi) }))}
                >
                  <X className="h-3.5 w-3.5 text-destructive" />
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {(node.children ?? []).map((c, ci) => (
        <NodeEditor key={ci} node={c} path={[...path, ci]} onChange={onChange} />
      ))}
    </div>
  )
}

function KVList({
  label, entries, onChange,
}: {
  label: string
  entries: Record<string, string>
  onChange: (entries: Record<string, string>) => void
}) {
  // Local row state with stable identity: editing a key must never
  // collapse/lose another row mid-typing (renaming 'a' to 'b' while a
  // 'b' row exists would otherwise silently drop one). Duplicates are
  // flagged; the LAST row wins in the emitted object.
  const [rows, setRows] = useState<Array<{ k: string; v: string }>>(
    () => Object.entries(entries).map(([k, v]) => ({ k, v })),
  )
  const emit = (next: Array<{ k: string; v: string }>) => {
    setRows(next)
    const obj: Record<string, string> = {}
    next.forEach(({ k, v }) => { if (k.trim() !== '') obj[k] = v })
    onChange(obj)
  }
  const keys = rows.map((r) => r.k).filter((k) => k.trim() !== '')
  const hasDupes = new Set(keys).size !== keys.length

  return (
    <div>
      <div className="mb-1 flex items-center justify-between">
        <span className="font-medium">{label}</span>
        <Button size="sm" variant="ghost" onClick={() => emit([...rows, { k: '', v: '' }])}>
          <Plus className="h-3 w-3" /> field
        </Button>
      </div>
      {hasDupes && (
        <p className="mb-1 text-destructive">Duplicate keys — the last one wins.</p>
      )}
      {rows.map((row, i) => (
        <div key={i} className="mb-1 flex items-center gap-1.5">
          <Input
            value={row.k}
            placeholder="key"
            aria-label="Metadata key"
            onChange={(e) => emit(rows.map((r, ri) => (ri === i ? { ...r, k: e.target.value } : r)))}
            className="h-7 w-32 text-xs"
          />
          <Input
            value={row.v}
            placeholder="value (may use {{vars}})"
            aria-label="Metadata value"
            onChange={(e) => emit(rows.map((r, ri) => (ri === i ? { ...r, v: e.target.value } : r)))}
            className="h-7 flex-1 text-xs"
          />
          <button type="button" aria-label="Remove field" onClick={() => emit(rows.filter((_, ri) => ri !== i))}>
            <X className="h-3.5 w-3.5 text-destructive" />
          </button>
        </div>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------
// provision
// ---------------------------------------------------------------------

interface FolderRow { id: string; name: string }

function ProvisionDialog({ template, onClose }: { template: WorkspaceTemplate; onClose: () => void }) {
  const navigate = useNavigate()
  const { data: workspaces } = useWorkspaces()
  const [workspaceId, setWorkspaceId] = useState('')
  const [parentFolderId, setParentFolderId] = useState('')
  const vars = useMemo(() => extractVariables(template.definition ?? { nodes: [] }), [template])
  const [values, setValues] = useState<Record<string, string>>({})

  const { data: folders } = useQuery({
    queryKey: ['provision-folders', workspaceId],
    queryFn: async () => {
      const { data } = await api.get<{ folders?: FolderRow[] } | FolderRow[]>(
        `/workspaces/${workspaceId}/folders`, { params: { page_size: 200 } },
      )
      return Array.isArray(data) ? data : data.folders ?? []
    },
    enabled: !!workspaceId,
  })

  const provision = useAppMutation({
    mutationFn: () => provisionTemplate(template.id, {
      workspace_id: workspaceId,
      parent_folder_id: parentFolderId || undefined,
      variables: values,
    }),
    onSuccess: (res) => {
      toast.success(`Provisioned ${res.folders_created} folders and ${res.docs_created} documents`)
      onClose()
      void navigate({ to: '/workspaces/$workspaceId', params: { workspaceId } })
    },
    defaultErrorMessage: 'Provisioning failed',
  })

  const ready = !!workspaceId && vars.every((v) => (values[v] ?? '').trim() !== '')

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={`New from "${template.name}"`}>
      <div className="space-y-3">
        <label className="block text-sm">
          <span className="mb-1 block text-xs font-medium">Workspace</span>
          <select
            value={workspaceId}
            onChange={(e) => { setWorkspaceId(e.target.value); setParentFolderId('') }}
            className="w-full rounded-md border border-border bg-background px-2 py-1.5"
            data-testid="provision-workspace"
          >
            <option value="">— pick a workspace —</option>
            {(workspaces ?? []).map((w: { id: string; name: string }) => (
              <option key={w.id} value={w.id}>{w.name}</option>
            ))}
          </select>
        </label>

        {workspaceId && (
          <label className="block text-sm">
            <span className="mb-1 block text-xs font-medium">Parent folder (optional — root if empty)</span>
            <select
              value={parentFolderId}
              onChange={(e) => setParentFolderId(e.target.value)}
              className="w-full rounded-md border border-border bg-background px-2 py-1.5"
            >
              <option value="">Workspace root</option>
              {(folders ?? []).map((f) => (
                <option key={f.id} value={f.id}>{f.name}</option>
              ))}
            </select>
          </label>
        )}

        {vars.length > 0 && (
          <div>
            <p className="mb-1 text-xs font-medium">Template variables</p>
            {vars.map((v) => (
              <Input
                key={v}
                label={v}
                value={values[v] ?? ''}
                onChange={(e) => setValues((prev) => ({ ...prev, [v]: e.target.value }))}
                data-testid={`provision-var-${v}`}
              />
            ))}
          </div>
        )}

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button
            onClick={() => provision.mutate()}
            disabled={!ready || provision.isPending}
            data-testid="provision-confirm"
          >
            <Rocket className="h-4 w-4" /> Provision
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
