// Workspace templates (ADR 0118): gallery + structured tree editor +
// "new from template" provision dialog with variable prompts.
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
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
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const { data: templates, isLoading } = useQuery({ queryKey: ['templates'], queryFn: listTemplates })
  const [editing, setEditing] = useState<WorkspaceTemplate | 'new' | null>(null)
  const [provisioning, setProvisioning] = useState<WorkspaceTemplate | null>(null)

  const refresh = () => void qc.invalidateQueries({ queryKey: ['templates'] })

  const del = useAppMutation({
    mutationFn: (id: string) => deleteTemplate(id),
    onSuccess: () => {
      toast.success(t('tpl.deleted'))
      refresh()
    },
    defaultErrorMessage: t('tpl.err_delete'),
  })

  return (
    <div className="p-6">
      <PageHeader
        title={t('tpl.title')}
        description={t('tpl.description')}
        actions={
          <Button onClick={() => setEditing('new')} data-testid="new-template">
            <Plus className="h-4 w-4" /> {t('tpl.new')}
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
          title={t('tpl.empty')}
          description={t('tpl.empty_desc')}
        />
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {(templates ?? []).map((tpl) => {
            const counts = countTree(tpl.definition?.nodes ?? [])
            const vars = extractVariables(tpl.definition ?? { nodes: [] })
            return (
              <div key={tpl.id} className="flex flex-col rounded-lg border border-border bg-card p-4" data-testid={`template-card-${tpl.id}`}>
                <div className="mb-1 flex items-center gap-2">
                  <FolderTree className="h-4 w-4 text-primary" />
                  <span className="font-medium">{tpl.name}</span>
                </div>
                {tpl.description ? (
                  <p className="mb-2 line-clamp-2 text-sm text-muted-foreground">{tpl.description}</p>
                ) : null}
                <div className="mb-3 flex flex-wrap gap-1.5">
                  <Badge variant="secondary">{t('tpl.folders', { count: counts.folders })}</Badge>
                  {counts.docs > 0 && <Badge variant="secondary">{t('tpl.docs', { count: counts.docs })}</Badge>}
                  {vars.map((v) => <Badge key={v} variant="outline" dir="ltr">{'{{'}{v}{'}}'}</Badge>)}
                </div>
                <div className="mt-auto flex gap-2">
                  <Button size="sm" onClick={() => setProvisioning(tpl)} data-testid={`use-template-${tpl.id}`}>
                    <Rocket className="h-4 w-4" /> {t('tpl.use')}
                  </Button>
                  <Button size="sm" variant="outline" onClick={() => setEditing(tpl)}>
                    <Edit className="h-4 w-4" /> {t('tpl.edit')}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t('tpl.delete_aria', { name: tpl.name })}
                    onClick={() => { if (confirm(t('tpl.delete_confirm', { name: tpl.name }))) del.mutate(tpl.id) }}
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
  const { t } = useTranslation('common')
  const [name, setName] = useState(template?.name ?? '')
  const [description, setDescription] = useState(template?.description ?? '')
  const [nodes, setNodes] = useState<TemplateNode[]>(
    // The default node name CARRIES a template variable as literal text, so
    // the placeholder is passed through rather than interpolated away.
    template?.definition?.nodes ?? [{ name: t('tpl.default_root', { placeholder: '{{project_name}}' }), children: [] }],
  )

  const save = useAppMutation({
    mutationFn: () => {
      const input = { name, description, definition: { nodes } }
      return template ? updateTemplate(template.id, input) : createTemplate(input)
    },
    onSuccess: () => { toast.success(t('tpl.saved')); onSaved() },
    defaultErrorMessage: t('tpl.err_save'),
  })

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={template ? t('tpl.edit_title') : t('tpl.new_title')} size="lg">
      <div className="space-y-3">
        <Input label={t('tpl.name')} value={name} onChange={(e) => setName(e.target.value)} data-testid="template-name" />
        <Input label={t('tpl.desc')} value={description} onChange={(e) => setDescription(e.target.value)} />
        <div>
          <div className="mb-1 flex items-center justify-between">
            <p className="text-xs font-medium">
              {t('tpl.tree_hint', { placeholder: '{{variable}}' })}
            </p>
            <Button
              size="sm"
              variant="outline"
              onClick={() => setNodes((ns) => [...ns, { name: t('tpl.new_folder') }])}
              data-testid="add-root-folder"
            >
              <Plus className="h-3.5 w-3.5" /> {t('tpl.root_folder')}
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
              <p className="p-2 text-xs text-muted-foreground">{t('tpl.need_root')}</p>
            )}
          </div>
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>{t('tpl.cancel')}</Button>
          <Button
            onClick={() => save.mutate()}
            disabled={save.isPending || !name.trim() || nodes.length === 0}
            data-testid="template-save"
          >
            {t('tpl.save')}
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
  const { t } = useTranslation('common')
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
          aria-label={open
            ? t('tpl.collapse_folder', { name: node.name || t('tpl.folder_fallback') })
            : t('tpl.expand_folder', { name: node.name || t('tpl.folder_fallback') })}
        >
          {open ? <ChevronDown className="h-3.5 w-3.5" /> : <DirectionalIcon name="ChevronRight" className="h-3.5 w-3.5" />}
        </button>
        <Input
          value={node.name}
          onChange={(e) => set((n) => ({ ...n, name: e.target.value }))}
          aria-label={t('tpl.folder_name')}
          className="h-8 flex-1 text-sm"
          data-testid={`node-name-${path.join('-')}`}
        />
        <Button
          size="sm"
          variant="ghost"
          title={t('tpl.add_subfolder')}
          onClick={() => set((n) => ({ ...n, children: [...(n.children ?? []), { name: t('tpl.new_folder') }] }))}
        >
          <Plus className="h-3.5 w-3.5" />
        </Button>
        <Button size="sm" variant="ghost" title={t('tpl.remove_folder')} onClick={() => onChange(path, () => null)}>
          <Trash2 className="h-3.5 w-3.5 text-destructive" />
        </Button>
      </div>

      {open && (
        <div className="ms-6 mt-1 space-y-2 rounded-md border border-border bg-muted/30 p-2 text-xs">
          <label className="flex items-center gap-2">
            {t('tpl.visibility')}
            <select
              value={node.visibility ?? 'shared'}
              onChange={(e) => set((n) => ({ ...n, visibility: e.target.value === 'private' ? 'private' : undefined }))}
              className="rounded border border-border bg-background px-1 py-0.5"
            >
              <option value="shared">{t('tpl.v_shared')}</option>
              <option value="private">{t('tpl.v_private')}</option>
            </select>
          </label>

          <KVList
            label={t('tpl.metadata_defaults')}
            entries={node.metadata ?? {}}
            onChange={(metadata) => set((n) => ({ ...n, metadata: Object.keys(metadata).length ? metadata : undefined }))}
          />

          <div>
            <div className="mb-1 flex items-center justify-between">
              <span className="font-medium">{t('tpl.placeholder_docs')}</span>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => set((n) => ({ ...n, docs: [...(n.docs ?? []), { title: t('tpl.new_doc') }] }))}
              >
                <Plus className="h-3 w-3" /> {t('tpl.add_doc')}
              </Button>
            </div>
            {(node.docs ?? []).map((d, di) => (
              <div key={di} className="mb-1 flex items-center gap-1.5">
                <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                <Input
                  value={d.title}
                  placeholder={t('tpl.doc_title')}
                  aria-label={t('tpl.doc_title_aria')}
                  onChange={(e) => set((n) => {
                    const docs = [...(n.docs ?? [])]
                    docs[di] = { ...docs[di], title: e.target.value }
                    return { ...n, docs }
                  })}
                  className="h-7 flex-1 text-xs"
                />
                <Input
                  value={(d.tags ?? []).join(', ')}
                  placeholder={t('tpl.doc_tags')}
                  aria-label={t('tpl.doc_tags_aria')}
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
                  aria-label={t('tpl.remove_doc')}
                  onClick={() => set((n) => ({ ...n, docs: (n.docs ?? []).filter((_, i) => i !== di) }))}
                >
                  <X className="h-3.5 w-3.5 text-destructive" />
                </button>
              </div>
            ))}
          </div>

          <div>
            <div className="mb-1 flex items-center justify-between">
              <span className="font-medium">{t('tpl.grants')}</span>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => set((n) => ({ ...n, grants: [...(n.grants ?? []), { grantee_type: 'group', grantee_id: '' }] }))}
              >
                <Plus className="h-3 w-3" /> {t('tpl.add_grant')}
              </Button>
            </div>
            {(node.grants ?? []).map((g, gi) => (
              <div key={gi} className="mb-1 flex items-center gap-1.5">
                <select
                  aria-label={t('tpl.grantee_type')}
                  value={g.grantee_type}
                  onChange={(e) => set((n) => {
                    const grants = [...(n.grants ?? [])]
                    grants[gi] = { ...grants[gi], grantee_type: e.target.value as 'user' | 'group' }
                    return { ...n, grants }
                  })}
                  className="rounded border border-border bg-background px-1 py-0.5"
                >
                  <option value="group">{t('tpl.g_group')}</option>
                  <option value="user">{t('tpl.g_user')}</option>
                </select>
                <Input
                  value={g.grantee_id}
                  dir="ltr"
                  placeholder={t('tpl.grantee_ph')}
                  aria-label={t('tpl.grantee_aria')}
                  onChange={(e) => set((n) => {
                    const grants = [...(n.grants ?? [])]
                    grants[gi] = { ...grants[gi], grantee_id: e.target.value }
                    return { ...n, grants }
                  })}
                  className="h-7 flex-1 font-mono text-xs"
                />
                <button
                  type="button"
                  aria-label={t('tpl.remove_grant')}
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
  const { t } = useTranslation('common')
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
          <Plus className="h-3 w-3" /> {t('tpl.kv_field')}
        </Button>
      </div>
      {hasDupes && (
        <p className="mb-1 text-destructive">{t('tpl.dup_keys')}</p>
      )}
      {rows.map((row, i) => (
        <div key={i} className="mb-1 flex items-center gap-1.5">
          <Input
            value={row.k}
            placeholder={t('tpl.kv_key')}
            aria-label={t('tpl.kv_key_aria')}
            onChange={(e) => emit(rows.map((r, ri) => (ri === i ? { ...r, k: e.target.value } : r)))}
            className="h-7 w-32 text-xs"
          />
          <Input
            value={row.v}
            placeholder={t('tpl.kv_value', { placeholder: '{{vars}}' })}
            aria-label={t('tpl.kv_value_aria')}
            onChange={(e) => emit(rows.map((r, ri) => (ri === i ? { ...r, v: e.target.value } : r)))}
            className="h-7 flex-1 text-xs"
          />
          <button type="button" aria-label={t('tpl.remove_field')} onClick={() => emit(rows.filter((_, ri) => ri !== i))}>
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
  const { t } = useTranslation('common')
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
      toast.success(t('tpl.provisioned', { folders: res.folders_created, docs: res.docs_created }))
      onClose()
      void navigate({ to: '/workspaces/$workspaceId', params: { workspaceId } })
    },
    defaultErrorMessage: t('tpl.err_provision'),
  })

  const ready = !!workspaceId && vars.every((v) => (values[v] ?? '').trim() !== '')

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('tpl.provision_title', { name: template.name })}>
      <div className="space-y-3">
        <label className="block text-sm">
          <span className="mb-1 block text-xs font-medium">{t('tpl.workspace')}</span>
          <select
            value={workspaceId}
            onChange={(e) => { setWorkspaceId(e.target.value); setParentFolderId('') }}
            className="w-full rounded-md border border-border bg-background px-2 py-1.5"
            data-testid="provision-workspace"
          >
            <option value="">{t('tpl.pick_workspace')}</option>
            {(workspaces ?? []).map((w: { id: string; name: string }) => (
              <option key={w.id} value={w.id}>{w.name}</option>
            ))}
          </select>
        </label>

        {workspaceId && (
          <label className="block text-sm">
            <span className="mb-1 block text-xs font-medium">{t('tpl.parent_folder')}</span>
            <select
              value={parentFolderId}
              onChange={(e) => setParentFolderId(e.target.value)}
              className="w-full rounded-md border border-border bg-background px-2 py-1.5"
            >
              <option value="">{t('tpl.ws_root')}</option>
              {(folders ?? []).map((f) => (
                <option key={f.id} value={f.id}>{f.name}</option>
              ))}
            </select>
          </label>
        )}

        {vars.length > 0 && (
          <div>
            <p className="mb-1 text-xs font-medium">{t('tpl.variables')}</p>
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
          <Button variant="ghost" onClick={onClose}>{t('tpl.cancel')}</Button>
          <Button
            onClick={() => provision.mutate()}
            disabled={!ready || provision.isPending}
            data-testid="provision-confirm"
          >
            <Rocket className="h-4 w-4" /> {t('tpl.provision')}
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
