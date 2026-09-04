// RelationshipsGraph — Relationships tab on doc detail (ADR 0099 §18 F4).
//
// Renders the (doc, depth=2) subgraph via cytoscape.js + dagre layout.
// Click a node → navigate to that doc. Click an edge → open a metadata
// side panel. Admin/owner gets an "Add edge" affordance for manual
// linking until the Phase 2 extractor lands.
import { useEffect, useMemo, useRef, useState } from 'react'
import { formatDateTime } from '@/lib/formatters'
import cytoscape, { type Core, type ElementDefinition } from 'cytoscape'
import dagre from 'cytoscape-dagre'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useNavigate } from '@tanstack/react-router'
import { AlertTriangle, Network, Plus, RotateCcw, Search as SearchIcon, Trash2 } from 'lucide-react'
import { toast } from 'sonner'

import {
  getContractGraph,
  createContractEdge,
  deleteContractEdge,
  type EdgeType,
  type GraphEdge,
} from '@/api/contractGraph'
import { api } from '@/api/client'
import { Spinner } from '@/components/ui/Spinner'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Dialog } from '@/components/ui/Dialog'
import { useAuthStore } from '@/store/authStore'

// Register dagre layout once at module load.
cytoscape.use(dagre)

interface Props {
  documentId: string
  workspaceId: string
}

export function RelationshipsGraph({ documentId, workspaceId }: Props) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const containerRef = useRef<HTMLDivElement>(null)
  const cyRef = useRef<Core | null>(null)
  const [selectedEdge, setSelectedEdge] = useState<GraphEdge | null>(null)
  const [addOpen, setAddOpen] = useState(false)

  const role = useAuthStore((s) => s.user?.role)
  const isAdmin = role === 'owner' || role === 'admin'

  const { data, isLoading, error, refetch, isFetching } = useQuery({
    queryKey: ['contract-graph', documentId],
    queryFn: () => getContractGraph(documentId, 2),
  })

  const invalidate = () => qc.invalidateQueries({ queryKey: ['contract-graph', documentId] })

  // Deleting an edge only works for edges where `src === documentId`
  // (the backend enforces this via the `src_document` predicate in
  // its DELETE statement). For inbound edges we hide the button — the
  // admin would need to navigate to the source doc to remove it.
  const deleteMut = useAppMutation({
    mutationFn: (edge: GraphEdge) => deleteContractEdge(edge.src, edge.id),
    onSuccess: () => {
      toast.success('Edge removed')
      setSelectedEdge(null)
      invalidate()
    },
    onError: (e: unknown) => toast.error(`Delete failed: ${(e as Error).message ?? String(e)}`),
  })

  const elements: ElementDefinition[] = useMemo(() => {
    if (!data) return []
    const els: ElementDefinition[] = []
    for (const n of data.nodes) {
      els.push({
        group: 'nodes',
        data: { id: n.id, label: truncate(n.title), isRoot: n.is_root ? 1 : 0 },
      })
    }
    for (const e of data.edges) {
      els.push({
        group: 'edges',
        data: { id: e.id, source: e.src, target: e.dst, label: e.type, edge: e },
      })
    }
    return els
  }, [data])

  useEffect(() => {
    if (!containerRef.current || elements.length === 0) return
    const cy = cytoscape({
      container: containerRef.current,
      elements,
      layout: { name: 'dagre', rankDir: 'TB', nodeSep: 60, rankSep: 80 } as any,
      style: [
        {
          selector: 'node',
          style: {
            label: 'data(label)',
            'text-valign': 'center',
            'text-halign': 'center',
            'font-size': '11px',
            'text-wrap': 'wrap',
            'text-max-width': '120px',
            width: 130,
            height: 36,
            shape: 'roundrectangle',
            'background-color': '#1e293b',
            color: '#fff',
            'border-width': 1,
            'border-color': '#475569',
          },
        },
        {
          selector: 'node[isRoot = 1]',
          style: {
            'background-color': '#059669',
            'border-color': '#10b981',
            'border-width': 2,
          },
        },
        {
          selector: 'edge',
          style: {
            label: 'data(label)',
            'font-size': '10px',
            color: '#94a3b8',
            'curve-style': 'bezier',
            'target-arrow-shape': 'triangle',
            'line-color': '#64748b',
            'target-arrow-color': '#64748b',
            'text-rotation': 'autorotate' as any,
            'text-margin-y': -8,
          },
        },
        {
          selector: 'edge[label = "supersedes"]',
          style: { 'line-color': '#dc2626', 'target-arrow-color': '#dc2626' },
        },
        {
          selector: 'edge[label = "amends"]',
          style: { 'line-color': '#2563eb', 'target-arrow-color': '#2563eb' },
        },
      ],
    })
    cy.on('tap', 'node', (evt) => {
      const id = evt.target.data('id')
      if (id && id !== documentId) {
        navigate({
          to: '/workspaces/$workspaceId/documents/$documentId',
          params: { workspaceId, documentId: id },
        })
      }
    })
    cy.on('tap', 'edge', (evt) => {
      const edge = evt.target.data('edge') as GraphEdge | undefined
      if (edge) setSelectedEdge(edge)
    })
    cyRef.current = cy
    return () => {
      cy.destroy()
      cyRef.current = null
    }
  }, [elements, navigate, workspaceId, documentId])

  if (isLoading) return <div className="flex h-64 items-center justify-center"><Spinner /></div>
  if (error) {
    return (
      <div
        className="flex flex-col items-start gap-2 rounded-md border border-red-500/40 bg-red-50/60 p-4 text-sm dark:bg-red-950/20"
        data-testid="relationships-error"
      >
        <span>Failed to load relationships: {(error as Error).message}</span>
        <Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>
          <RotateCcw className="me-1 h-3 w-3" />
          {isFetching ? 'Retrying…' : 'Retry'}
        </Button>
      </div>
    )
  }
  if (!data || data.edges.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 rounded-md border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
        <Network className="h-8 w-8" />
        <p>No relationships yet.</p>
        <p className="text-xs">
          Edges are added by the future contract-reference extractor or
          manually by admins.
        </p>
        {isAdmin && (
          <Button variant="outline" size="sm" onClick={() => setAddOpen(true)}>
            <Plus className="me-1 h-3.5 w-3.5" /> Add edge
          </Button>
        )}
        {addOpen && (
          <AddEdgeDialog
            documentId={documentId}
            onClose={() => setAddOpen(false)}
            onCreated={() => { setAddOpen(false); invalidate() }}
          />
        )}
      </div>
    )
  }

  return (
    <div className="space-y-3">
      {isAdmin && (
        <div className="flex items-center justify-end">
          <Button variant="outline" size="sm" onClick={() => setAddOpen(true)}>
            <Plus className="me-1 h-3.5 w-3.5" /> Add edge
          </Button>
        </div>
      )}
      {data.truncated && (
        <div className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-2 text-xs text-warning-strong">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5" />
          <span>
            Graph truncated to 500 nodes — increase depth carefully or filter by edge type.
          </span>
        </div>
      )}
      <div
        ref={containerRef}
        className="h-[480px] w-full rounded-md border border-border bg-card"
        data-testid="contract-graph"
      />
      {selectedEdge && (
        <aside className="rounded-md border border-border bg-card p-3 text-sm">
          <div className="flex items-center justify-between">
            <strong className="capitalize">{selectedEdge.type}</strong>
            <button
              className="text-xs text-muted-foreground hover:underline"
              onClick={() => setSelectedEdge(null)}
            >
              close
            </button>
          </div>
          <dl className="mt-2 space-y-1 text-xs">
            <Row label="Confidence" value={`${(selectedEdge.confidence * 100).toFixed(0)}%`} />
            <Row label="Created" value={formatDateTime(selectedEdge.created_at)} />
            {Object.entries(selectedEdge.metadata).map(([k, v]) => (
              <Row key={k} label={k} value={String(v)} />
            ))}
          </dl>
          {/* Delete is only allowed for outbound edges (src === current
              doc); the backend rejects DELETE with src_document mismatch
              and inbound edges should be removed from the source doc's
              page. */}
          {isAdmin && selectedEdge.src === documentId && (
            <div className="mt-3 flex justify-end">
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  if (confirm(`Remove this "${selectedEdge.type}" edge?`)) {
                    deleteMut.mutate(selectedEdge)
                  }
                }}
                disabled={deleteMut.isPending}
              >
                <Trash2 className="me-1 h-3.5 w-3.5 text-destructive" />
                Delete edge
              </Button>
            </div>
          )}
        </aside>
      )}

      {addOpen && (
        <AddEdgeDialog
          documentId={documentId}
          onClose={() => setAddOpen(false)}
          onCreated={() => { setAddOpen(false); invalidate() }}
        />
      )}
    </div>
  )
}

interface SearchHit {
  document_id: string
  title?: string
  workspace_name?: string
  mime_type?: string
}

// AddEdgeDialog — Phase-1 manual edge creation (ADR 0099). Picks a
// target doc via /search and asks the user to label the relationship.
// The backend UPSERTs on the (src, dst, edge_type) triplet so a
// re-submit of the same edge is idempotent rather than failing.
function AddEdgeDialog({
  documentId,
  onClose,
  onCreated,
}: {
  documentId: string
  onClose: () => void
  onCreated: () => void
}) {
  const [pickerQ, setPickerQ] = useState('')
  const [targetId, setTargetId] = useState<string | null>(null)
  const [targetTitle, setTargetTitle] = useState('')
  const [edgeType, setEdgeType] = useState<EdgeType>('references')
  const [note, setNote] = useState('')

  const search = useQuery({
    queryKey: ['edge-picker', pickerQ],
    queryFn: async () => {
      if (pickerQ.trim().length < 2) return [] as SearchHit[]
      const { data } = await api.get<{ results?: SearchHit[]; hits?: SearchHit[] } | SearchHit[]>(
        `/search?q=${encodeURIComponent(pickerQ)}&limit=10`,
      )
      const list = Array.isArray(data) ? data : data?.results ?? data?.hits ?? []
      return list.filter((h) => h.document_id && h.document_id !== documentId)
    },
    enabled: pickerQ.trim().length >= 2,
  })

  const mut = useAppMutation({
    mutationFn: () => createContractEdge({
      documentId,
      targetDocumentId: targetId!,
      edgeType,
      metadata: note ? { note, source: 'manual' } : { source: 'manual' },
    }),
    onSuccess: () => {
      toast.success('Edge added')
      onCreated()
    },
    onError: (e: unknown) => toast.error(`Add failed: ${(e as Error).message ?? String(e)}`),
  })

  return (
    <Dialog
      open
      onOpenChange={(o) => { if (!o) onClose() }}
      title="Add relationship"
      description="Manually link this document to another. Links you add here always take precedence over any automatically detected ones."
      size="lg"
    >
      <div className="space-y-3">
        <div className="space-y-1">
          <label className="text-xs font-semibold text-muted-foreground">Target document</label>
          <div className="flex items-center gap-2 rounded-md border border-border bg-card px-2">
            <SearchIcon className="h-4 w-4 text-muted-foreground" />
            <Input
              value={pickerQ}
              onChange={(e) => setPickerQ(e.target.value)}
              placeholder="Search by title…"
              className="border-0 bg-transparent focus-visible:ring-0"
            />
          </div>
          {pickerQ.trim().length >= 2 && (
            <div className="max-h-48 overflow-y-auto rounded-md border border-border bg-background">
              {search.isLoading && (
                <div className="p-2 text-xs text-muted-foreground">Searching…</div>
              )}
              {!search.isLoading && (search.data ?? []).length === 0 && (
                <div className="p-2 text-xs text-muted-foreground">No matches.</div>
              )}
              {(search.data ?? []).map((h) => {
                const active = targetId === h.document_id
                return (
                  <button
                    key={h.document_id}
                    type="button"
                    onClick={() => {
                      setTargetId(h.document_id)
                      setTargetTitle(h.title ?? h.document_id)
                    }}
                    className={`flex w-full flex-col gap-0.5 border-b border-border px-2 py-1.5 text-start text-sm last:border-0 hover:bg-accent ${
                      active ? 'bg-accent' : ''
                    }`}
                  >
                    <span className="font-medium">{h.title || '(untitled)'}</span>
                    <span className="text-[11px] text-muted-foreground">
                      {h.workspace_name ?? '—'} · {h.mime_type ?? 'unknown'}
                    </span>
                  </button>
                )
              })}
            </div>
          )}
          {targetId && (
            <p className="text-xs text-muted-foreground">
              Selected: <strong className="text-foreground">{targetTitle}</strong>
            </p>
          )}
        </div>

        <div className="space-y-1">
          <label className="text-xs font-semibold text-muted-foreground">Relationship</label>
          <div className="flex gap-2">
            {(['references', 'amends', 'supersedes'] as EdgeType[]).map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => setEdgeType(t)}
                className={`flex-1 rounded-md border px-2 py-1.5 text-sm capitalize transition-colors ${
                  edgeType === t
                    ? 'border-violet-500/50 bg-violet-50/40 dark:bg-violet-950/15'
                    : 'border-border bg-card hover:bg-accent'
                }`}
              >
                {t}
              </button>
            ))}
          </div>
        </div>

        <Input
          label="Note (optional)"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="e.g. Section 5.2 — indemnification override"
        />

        <div className="flex items-center justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose} disabled={mut.isPending}>Cancel</Button>
          <Button
            onClick={() => mut.mutate()}
            disabled={!targetId || mut.isPending}
          >
            {mut.isPending ? <Spinner className="me-1 h-4 w-4" /> : <Plus className="me-1 h-4 w-4" />}
            Add edge
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline gap-2">
      <dt className="text-muted-foreground">{label}:</dt>
      <dd className="font-mono">{value}</dd>
    </div>
  )
}

function truncate(s: string, n = 32) {
  if (!s) return '(untitled)'
  return s.length > n ? s.slice(0, n - 1) + '…' : s
}
