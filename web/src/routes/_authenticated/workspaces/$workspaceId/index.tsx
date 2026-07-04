import { useMemo, useRef, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Upload, Settings as SettingsIcon, Search, FolderOpen, StickyNote } from 'lucide-react'

import { useDocuments } from '@/hooks/useDocuments'
import { useUpload } from '@/hooks/useUpload'
import { getFolder, getWorkspace, updateFolder } from '@/api/workspaces'
import { updateDocument, deleteDocument, getVersions, getDownloadURL, createNote, moveDocument } from '@/api/documents'
import { cn } from '@/lib/cn'
import { useCreateFolder, useFolders } from '@/hooks/useFolders'
import {
  DndContext, DragOverlay, PointerSensor, KeyboardSensor, useSensor, useSensors, pointerWithin,
  type DragStartEvent, type DragEndEvent,
} from '@dnd-kit/core'
import { DraggableTile, DroppableFolder, DragOverlayContent, type DragData, type DropData } from '@/components/folders/dnd'
import { readErrorMessage } from '@/api/client'
import { predictFiling, sendFilingFeedback, type PredictResponse } from '@/api/predictiveFiling'

import { FolderBreadcrumbs } from '@/components/folders/FolderBreadcrumbs'
import { NewFolderDialog } from '@/components/folders/NewFolderDialog'
import { FolderActionsMenu } from '@/components/folders/FolderActionsMenu'
import { FolderTile, FileTile, NewFolderTile } from '@/components/folders/BrowserTiles'
import { BrowserTreeSidebar } from '@/components/folders/BrowserTreeSidebar'
import { BrowserDetailsPanel, type Selection } from '@/components/folders/BrowserDetailsPanel'
import { DocumentActionsMenu } from '@/components/documents/DocumentActionsMenu'
import { DocumentViewerModal } from '@/components/documents/DocumentViewerModal'
import { MoveDocumentDialog } from '@/components/documents/MoveDocumentDialog'
import { BulkTagDialog } from '@/components/documents/BulkTagDialog'
import { BulkActionBar } from '@/components/documents/BulkActionBar'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { WorkspaceSettingsDialog } from '@/components/workspaces/WorkspaceSettingsDialog'
import { UploadEnrichmentDialog, type EnrichmentDecision } from '@/components/documents/UploadEnrichmentDialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Skeleton } from '@/components/ui/Skeleton'
import { useAuthStore } from '@/store/authStore'
import { toast } from 'sonner'
import { runBatched } from '@/lib/runBatched'
import type { Document as ApiDocument } from '@/types/api'

interface EnrichmentItem { file: File; docId: string; prediction: PredictResponse | null }

function WorkspacePage() {
  const { workspaceId } = Route.useParams()
  const search = Route.useSearch() as { doc?: string; folder?: string }
  const navigate = useNavigate({ from: '/workspaces/$workspaceId/' })
  const currentFolderId = search.folder ?? null

  const closeViewer = () => navigate({ search: (s: Record<string, unknown>) => ({ ...s, doc: undefined }) })
  const openDoc = (id: string) => navigate({ search: (s: Record<string, unknown>) => ({ ...s, doc: id }) })
  const navigateToFolder = (folderId: string | null) => {
    setSelection(null)
    setSelectedIds(new Set())
    navigate({ search: (s: Record<string, unknown>) => ({ ...s, folder: folderId ?? undefined }) })
  }

  const ws = useQuery({ queryKey: ['workspace', workspaceId], queryFn: () => getWorkspace(workspaceId), staleTime: 60_000 })
  const { data: foldersData, isLoading: foldersLoading } = useFolders(workspaceId, currentFolderId ?? undefined)
  const folderDetail = useQuery({ queryKey: ['folder', currentFolderId], queryFn: () => getFolder(currentFolderId!), enabled: !!currentFolderId })
  const documentsParams: Record<string, string> = currentFolderId ? { folder_id: currentFolderId } : {}
  const { data, isLoading } = useDocuments(workspaceId, documentsParams)
  const { uploadFiles } = useUpload(workspaceId, currentFolderId ?? undefined)
  const qc = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [newFolderOpen, setNewFolderOpen] = useState(false)
  const [dragOver, setDragOver] = useState(false)
  const [query, setQuery] = useState('')
  const [selection, setSelection] = useState<Selection>(null)
  // Multi-select for bulk operations (separate from the single-select
  // details panel above).
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  // Anchor row for shift-click range selection (index into shownDocs order).
  const [anchorId, setAnchorId] = useState<string | null>(null)
  // Drag-and-drop: active overlay preview + ids optimistically hidden while a
  // move is in flight (restored on failure → rollback). Pointer needs 6px of
  // travel before a drag starts so click-to-select still works; KeyboardSensor
  // is a bonus — the MoveDocumentDialog is the primary keyboard-accessible move.
  const [activeDrag, setActiveDrag] = useState<{ label: string; count: number } | null>(null)
  const [movingIds, setMovingIds] = useState<Set<string>>(new Set())
  const dndSensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor),
  )
  const [bulkMoveOpen, setBulkMoveOpen] = useState(false)
  const [bulkTagOpen, setBulkTagOpen] = useState(false)
  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false)
  const [bulkBusy, setBulkBusy] = useState(false)
  const createFolder = useCreateFolder()
  const dragCounter = useRef(0)

  const role = useAuthStore((s) => s.user?.role)
  const isAdmin = role === 'owner' || role === 'admin'

  const folders = foldersData ?? []
  const docs = useMemo(
    () => ((data as unknown as { documents?: unknown[]; items?: unknown[] })?.documents
      ?? (data as unknown as { items?: unknown[] })?.items
      ?? []) as ApiDocument[],
    [data],
  )
  const q = query.trim().toLowerCase()
  const shownFolders = q ? folders.filter((f) => f.name.toLowerCase().includes(q)) : folders
  const shownDocs = q ? docs.filter((d) => d.title.toLowerCase().includes(q)) : docs
  const usedBytes = useMemo(() => docs.reduce((sum, d) => sum + (Number(d.total_size_bytes) || 0), 0), [docs])

  // ── multi-select bulk operations ──
  const selectedDocs = useMemo(() => docs.filter((d) => selectedIds.has(d.id)), [docs, selectedIds])
  const toggleSelect = (id: string) =>
    setSelectedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  const clearSelection = () => { setSelectedIds(new Set()); setAnchorId(null) }

  // File-manager selection: shift-click selects the contiguous range from the
  // anchor in display order; ctrl/cmd or a plain click toggles one row and
  // re-anchors. The MouseEvent carries the modifier keys (checkbox onChange
  // doesn't), so the work happens in onClick.
  const handleSelectClick = (id: string, e: React.MouseEvent) => {
    if (e.shiftKey && anchorId) {
      const ids = shownDocs.map((d) => d.id)
      const a = ids.indexOf(anchorId)
      const b = ids.indexOf(id)
      if (a !== -1 && b !== -1) {
        const [lo, hi] = a < b ? [a, b] : [b, a]
        setSelectedIds((prev) => {
          const next = new Set(prev)
          for (let i = lo; i <= hi; i++) next.add(ids[i])
          return next
        })
        return
      }
    }
    toggleSelect(id)
    setAnchorId(id)
  }

  const refreshAfterBulk = () => {
    qc.invalidateQueries({ queryKey: ['documents', workspaceId] })
    qc.invalidateQueries({ queryKey: ['folders', workspaceId] })
    qc.invalidateQueries({ queryKey: ['workspace', workspaceId] })
  }

  // docTitle resolves a selected id to a human label for failure summaries.
  const docTitle = (id: string) => docs.find((d) => d.id === id)?.title ?? id

  const bulkDelete = async () => {
    const ids = [...selectedIds]
    if (ids.length === 0) return
    setBulkBusy(true)
    const tid = toast.loading(`Deleting 0/${ids.length}…`)
    // Concurrency-capped so a large selection doesn't fire hundreds of
    // requests at once (freeze) — progress updates as each completes.
    const results = await runBatched(ids, (id) => deleteDocument(id), {
      concurrency: 5,
      onProgress: (done, total) => toast.loading(`Deleting ${done}/${total}…`, { id: tid }),
    })
    setBulkBusy(false)
    const failed = results.filter((r) => !r.ok)
    if (failed.length === 0) {
      toast.success(`Deleted ${ids.length} document${ids.length === 1 ? '' : 's'}`, { id: tid })
    } else {
      const okCount = ids.length - failed.length
      toast.error(`${okCount}/${ids.length} deleted — ${failed.length} failed`, {
        id: tid,
        description: failed.slice(0, 5).map((f) => docTitle(ids[f.index])).join(', ') + (failed.length > 5 ? ', …' : ''),
      })
    }
    setBulkDeleteOpen(false)
    clearSelection()
    refreshAfterBulk()
  }

  // Download loops the single-document signed-URL path (no server-side zip
  // endpoint). Kept sequential so the browser doesn't drop concurrent
  // navigations; a live progress toast + per-item failure summary keep
  // large selections legible.
  const bulkDownload = async () => {
    const targets = selectedDocs
    if (targets.length === 0) return
    setBulkBusy(true)
    const tid = toast.loading(`Preparing 0/${targets.length} download${targets.length === 1 ? '' : 's'}…`)
    const failed: string[] = []
    let done = 0
    for (const d of targets) {
      try {
        const versions = await getVersions(d.id)
        const latest = [...(versions ?? [])].sort((a, b) => b.version_number - a.version_number)[0]
        if (!latest?.id) { failed.push(d.title); continue }
        const { url } = await getDownloadURL(d.id, latest.id)
        const a = document.createElement('a')
        a.href = url
        a.download = d.title
        document.body.appendChild(a)
        a.click()
        a.remove()
        await new Promise((r) => setTimeout(r, 400))
      } catch {
        failed.push(d.title)
      }
      done++
      toast.loading(`Preparing ${done}/${targets.length} download${targets.length === 1 ? '' : 's'}…`, { id: tid })
    }
    setBulkBusy(false)
    if (failed.length === 0) {
      toast.success(`Started ${targets.length} download${targets.length === 1 ? '' : 's'}`, { id: tid })
    } else {
      toast.error(`${targets.length - failed.length}/${targets.length} started — ${failed.length} failed`, {
        id: tid,
        description: failed.slice(0, 5).join(', ') + (failed.length > 5 ? ', …' : ''),
      })
    }
  }

  // Export the selected docs' metadata as NDJSON (one JSON object per line),
  // client-side from the rows already in memory. The server BulkExport is
  // resource-scoped (whole workspace/folder, ADR 0075), not a selection, so
  // this covers "export these N docs" with no backend change. Metadata only,
  // not blob content.
  const exportSelected = () => {
    const targets = selectedDocs
    if (targets.length === 0) return
    const ndjson = targets.map((d) => JSON.stringify(d)).join('\n')
    const blob = new Blob([ndjson], { type: 'application/x-ndjson' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `export-${targets.length}-documents.ndjson`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
    toast.success(`Exported ${targets.length} document${targets.length === 1 ? '' : 's'} (metadata)`)
  }

  // ── drag-and-drop move ──
  const onDragStart = (e: DragStartEvent) => {
    const data = e.active.data.current as DragData | undefined
    if (!data) return
    const count = data.kind === 'doc' && selectedIds.has(data.id) ? selectedIds.size : 1
    setActiveDrag({ label: data.label, count })
  }

  const onDragEnd = async (e: DragEndEvent) => {
    setActiveDrag(null)
    const data = e.active.data.current as DragData | undefined
    const target = e.over?.data.current as DropData | undefined
    if (!data || !target) return
    const targetFolderId = target.folderId
    if (data.kind === 'folder' && data.id === targetFolderId) return // can't drop a folder onto itself
    // Multi-item: dragging a *selected* doc moves the whole selection.
    const docIds = data.kind === 'doc' ? (selectedIds.has(data.id) ? [...selectedIds] : [data.id]) : []
    const folderIds = data.kind === 'folder' ? [data.id] : []
    const ops: Array<readonly ['doc' | 'folder', string]> = [
      ...docIds.map((id) => ['doc', id] as const),
      ...folderIds.map((id) => ['folder', id] as const),
    ]
    if (ops.length === 0) return

    setMovingIds(new Set(ops.map(([, id]) => id))) // optimistic: hide from the current view
    const tid = toast.loading(`Moving ${ops.length} item${ops.length === 1 ? '' : 's'}…`)
    const results = await runBatched(
      ops,
      async ([kind, id]) => {
        if (kind === 'doc') await moveDocument(id, targetFolderId)
        else await updateFolder(id, { new_parent_folder_id: targetFolderId })
      },
      { concurrency: 5 },
    )
    const failed = results.filter((r) => !r.ok)
    if (failed.length === 0) {
      toast.success(`Moved ${ops.length} item${ops.length === 1 ? '' : 's'}`, { id: tid })
    } else {
      // Server rejected some (e.g. folder into its own descendant) → rollback:
      // clearing movingIds un-hides them and the invalidation below restores truth.
      toast.error(`${ops.length - failed.length}/${ops.length} moved — ${failed.length} failed (rolled back)`, { id: tid })
    }
    clearSelection()
    setMovingIds(new Set())
    refreshAfterBulk() // resync tiles, counts, and breadcrumb from the server
  }

  // ── upload + predictive-filing enrichment (unchanged from the prior view) ──
  const [enrichmentQueue, setEnrichmentQueue] = useState<EnrichmentItem[]>([])
  const predictionsRef = useRef<Map<File, Promise<PredictResponse | null>>>(new Map())

  const startUpload = (files: File[]) => {
    if (!files.length) return
    for (const file of files) {
      const p = predictFiling({ filename: file.name, mime_type: file.type || 'application/octet-stream', workspace_id: workspaceId }).catch(() => null)
      predictionsRef.current.set(file, p)
    }
    uploadFiles(files, undefined, async (file, docId) => {
      const pred = (await predictionsRef.current.get(file)) ?? null
      predictionsRef.current.delete(file)
      setEnrichmentQueue((prev) => [...prev, { file, docId, prediction: pred }])
    })
  }
  const dequeueEnrichment = () => setEnrichmentQueue((prev) => prev.slice(1))

  const handleEnrichmentConfirm = async (item: EnrichmentItem, decision: EnrichmentDecision) => {
    dequeueEnrichment()
    try {
      await updateDocument(item.docId, { title: decision.title, document_class: decision.documentClass, tags: decision.tags })
      await qc.invalidateQueries({ queryKey: ['documents', workspaceId] })
    } catch (e) {
      toast.error(`Couldn't update ${item.file.name}: ${(e as Error).message}`)
    }
    if (item.prediction) {
      const p = item.prediction
      const predictedTags = p.suggested_tags.map((t) => t.tag)
      sendFilingFeedback({
        prediction_id: p.prediction_id,
        class_accepted: decision.documentClass === p.classification.predicted_class,
        folder_accepted: false,
        tags_accepted: decision.tags.filter((t) => predictedTags.includes(t)),
        tags_rejected: predictedTags.filter((t) => !decision.tags.includes(t)),
        final_class: decision.documentClass, final_tags: decision.tags,
        predicted_class: p.classification.predicted_class, predicted_class_score: p.classification.confidence,
        predicted_folder_id: p.suggested_folder?.id, predicted_folder_score: p.suggested_folder?.score ?? 0,
        predicted_tags: predictedTags, filename: item.file.name,
        mime_type: item.file.type || 'application/octet-stream', workspace_id: workspaceId,
      }).catch((e) => console.warn('filing feedback failed', e))
    }
  }
  const handleEnrichmentSkip = (item: EnrichmentItem) => {
    dequeueEnrichment()
    if (!item.prediction) return
    const p = item.prediction
    sendFilingFeedback({
      prediction_id: p.prediction_id, class_accepted: false, folder_accepted: false,
      tags_accepted: [], tags_rejected: p.suggested_tags.map((t) => t.tag),
      final_class: p.classification.predicted_class, final_tags: [],
      predicted_class: p.classification.predicted_class, predicted_class_score: p.classification.confidence,
      predicted_folder_id: p.suggested_folder?.id, predicted_folder_score: p.suggested_folder?.score ?? 0,
      predicted_tags: p.suggested_tags.map((t) => t.tag), filename: item.file.name,
      mime_type: item.file.type || 'application/octet-stream', workspace_id: workspaceId,
    }).catch((e) => console.warn('filing feedback failed', e))
  }

  // New note: create a note document in the current folder, then open the
  // collaborative editor on it (the ?doctype hint mounts the editor before
  // the first markdown version exists). Notes need a folder to live in.
  const onNewNote = async () => {
    if (!currentFolderId) return
    const note = await createNote({ workspace_id: workspaceId, folder_id: currentFolderId, doc_type: 'note' })
    navigate({
      to: '/workspaces/$workspaceId/documents/$documentId',
      params: { workspaceId, documentId: note.id },
      search: { doctype: 'note' },
    })
  }

  const onPick = () => fileInputRef.current?.click()
  const onChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files
    if (!files || files.length === 0) return
    startUpload(Array.from(files)); e.target.value = ''
  }
  const onDrop = (e: React.DragEvent) => {
    e.preventDefault(); dragCounter.current = 0; setDragOver(false)
    const files = Array.from(e.dataTransfer.files ?? [])
    if (files.length) startUpload(files)
  }

  const loadingTiles = (n: number) => Array.from({ length: n }).map((_, i) => <Skeleton key={i} className="h-[152px] rounded-2xl" />)
  const nothingHere = !foldersLoading && !isLoading && shownFolders.length === 0 && shownDocs.length === 0

  return (
    <div
      onDragEnter={(e) => {
        e.preventDefault()
        if (!Array.from(e.dataTransfer?.types ?? []).includes('Files')) return
        dragCounter.current += 1
        if (dragCounter.current === 1) setDragOver(true)
      }}
      onDragOver={(e) => e.preventDefault()}
      onDragLeave={(e) => { e.preventDefault(); dragCounter.current = Math.max(0, dragCounter.current - 1); if (dragCounter.current === 0) setDragOver(false) }}
      onDrop={onDrop}
      className="relative flex min-h-0 flex-1 overflow-hidden rounded-2xl border border-border bg-card shadow-sm"
    >
      <input ref={fileInputRef} type="file" multiple className="hidden" onChange={onChange} />

      <DndContext sensors={dndSensors} collisionDetection={pointerWithin} onDragStart={onDragStart} onDragEnd={onDragEnd} onDragCancel={() => setActiveDrag(null)}>
      {/* ── left: folder tree + storage ── */}
      <BrowserTreeSidebar
        workspaceId={workspaceId}
        currentFolderId={currentFolderId}
        onNavigate={navigateToFolder}
        workspace={ws.data}
        usedBytes={usedBytes}
      />

      {/* ── center ── */}
      <section className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center justify-between gap-3 border-b border-border px-6 py-3.5">
          <FolderBreadcrumbs
            ancestors={folderDetail.data?.ancestors ?? []}
            current={folderDetail.data ?? null}
            workspaceName={ws.data?.name ?? 'Workspace'}
            onNavigate={navigateToFolder}
          />
          {isAdmin && (
            <div className="flex flex-none items-center gap-2">
              <Button variant="ghost" size="sm" onClick={() => setSettingsOpen(true)} className="gap-2" data-testid="open-ws-settings">
                <SettingsIcon className="h-4 w-4" /> Settings
              </Button>
            </div>
          )}
        </header>

        <div className="flex items-center gap-3 px-6 pt-5">
          <div className="relative flex-1">
            <Search className="pointer-events-none absolute inset-y-0 start-4 my-auto h-[18px] w-[18px] text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search this folder…"
              className="h-12 rounded-xl border-border bg-muted/40 ps-11 focus-visible:bg-card"
            />
          </div>
          <Button
            onClick={onNewNote}
            disabled={!currentFolderId}
            variant="outline"
            className="h-12 gap-2 rounded-xl px-5"
            title={currentFolderId ? 'Create a collaborative note' : 'Open a folder to add a note'}
            data-testid="new-note"
          >
            <StickyNote className="h-[18px] w-[18px]" /> New note
          </Button>
          <Button onClick={onPick} className="h-12 gap-2 rounded-xl px-5 shadow-sm hover:shadow-md" data-testid="open-upload">
            <Upload className="h-[18px] w-[18px]" /> Upload
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-10 pt-5">
          <h2 className="mb-4 px-0.5 text-lg font-bold tracking-tight text-foreground">Folders</h2>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(162px,1fr))] gap-4">
            {foldersLoading ? loadingTiles(4) : (
              <>
                {shownFolders.filter((f) => !movingIds.has(f.id)).map((f) => (
                  <DroppableFolder key={f.id} folderId={f.id}>
                    <DraggableTile dndId={`folder:${f.id}`} data={{ kind: 'folder', id: f.id, label: f.name }}>
                      <FolderActionsMenu folder={f} canManage={isAdmin} onOpen={() => navigateToFolder(f.id)}>
                        <FolderTile
                          folder={f}
                          selected={selection?.type === 'folder' && selection.folder.id === f.id}
                          onSelect={() => setSelection({ type: 'folder', folder: f })}
                          onOpen={() => navigateToFolder(f.id)}
                        />
                      </FolderActionsMenu>
                    </DraggableTile>
                  </DroppableFolder>
                ))}
                {!q && <NewFolderTile onClick={() => setNewFolderOpen(true)} />}
              </>
            )}
          </div>

          <h2 className="mb-4 mt-9 px-0.5 text-lg font-bold tracking-tight text-foreground">Files</h2>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(162px,1fr))] gap-4">
            {isLoading ? loadingTiles(4) : shownDocs.filter((d) => !movingIds.has(d.id)).map((d) => {
              const checked = selectedIds.has(d.id)
              return (
                <div key={d.id} className="group/sel relative">
                  {/* Accessible multi-select checkbox, overlaid top-start.
                      Visible on hover/focus, or whenever a selection is
                      active. stopPropagation so toggling doesn't open the
                      doc or change the details-panel selection. */}
                  <div
                    className={cn(
                      'absolute start-2 top-2 z-10 transition-opacity',
                      checked || selectedIds.size > 0
                        ? 'opacity-100'
                        : 'opacity-0 group-hover/sel:opacity-100 focus-within:opacity-100',
                    )}
                  >
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={() => {}}
                      onClick={(e) => { e.stopPropagation(); handleSelectClick(d.id, e) }}
                      aria-label={`Select ${d.title} (shift-click to select a range)`}
                      data-testid={`select-doc-${d.id}`}
                      className="h-4 w-4 cursor-pointer rounded border-border accent-primary"
                    />
                  </div>
                  <DraggableTile dndId={`doc:${d.id}`} data={{ kind: 'doc', id: d.id, label: d.title }}>
                    <DocumentActionsMenu doc={d} onOpen={() => openDoc(d.id)}>
                      <FileTile
                        doc={d}
                        selected={checked || (selection?.type === 'file' && selection.doc.id === d.id)}
                        onSelect={() => setSelection({ type: 'file', doc: d })}
                        onOpen={() => openDoc(d.id)}
                      />
                    </DocumentActionsMenu>
                  </DraggableTile>
                </div>
              )
            })}
            {!isLoading && shownDocs.length === 0 && !nothingHere && (
              <p className="col-span-full py-6 text-sm text-muted-foreground">No files here yet.</p>
            )}
          </div>

          {nothingHere && (
            <div className="mt-10 flex flex-col items-center gap-3 rounded-2xl border border-dashed border-border py-14 text-center">
              <span className="grid h-12 w-12 place-items-center rounded-full bg-muted text-muted-foreground"><FolderOpen className="h-6 w-6" /></span>
              <p className="text-sm font-semibold text-foreground">{q ? 'No matches' : 'This folder is empty'}</p>
              <p className="max-w-xs text-xs text-muted-foreground">{q ? `Nothing matches "${query}".` : 'Drop files here or use Upload to add documents.'}</p>
            </div>
          )}
        </div>
      </section>
      <DragOverlay>{activeDrag ? <DragOverlayContent label={activeDrag.label} count={activeDrag.count} /> : null}</DragOverlay>
      </DndContext>

      {/* ── right: details ── */}
      <BrowserDetailsPanel
        selection={selection}
        workspace={ws.data}
        onOpenFolder={(f) => navigateToFolder(f.id)}
        onOpenFile={(d) => openDoc(d.id)}
      />

      {/* ── dialogs + overlays (unchanged behaviour) ── */}
      <WorkspaceSettingsDialog open={settingsOpen} onOpenChange={setSettingsOpen} workspaceId={workspaceId} />

      <NewFolderDialog
        open={newFolderOpen}
        onOpenChange={setNewFolderOpen}
        parentName={folderDetail.data?.name ?? null}
        canPickVisibility
        isCreating={createFolder.isPending}
        onCreate={async (name, visibility) => {
          await new Promise<void>((resolve, reject) => {
            createFolder.mutate(
              { workspaceId, name, parentId: currentFolderId ?? undefined, visibility },
              {
                onSuccess: () => { toast.success('Folder created'); setNewFolderOpen(false); resolve() },
                onError: (e) => { toast.error(readErrorMessage(e) ?? 'Could not create folder'); reject(e as Error) },
              },
            )
          })
        }}
      />

      {enrichmentQueue.length > 0 && (
        <UploadEnrichmentDialog
          key={enrichmentQueue[0].docId}
          open
          file={enrichmentQueue[0].file}
          prediction={enrichmentQueue[0].prediction}
          onConfirm={(decision) => handleEnrichmentConfirm(enrichmentQueue[0], decision)}
          onSkip={() => handleEnrichmentSkip(enrichmentQueue[0])}
        />
      )}

      <DocumentViewerModal
        open={!!search.doc}
        onOpenChange={(o) => { if (!o) closeViewer() }}
        documentId={search.doc ?? null}
        workspaceId={workspaceId}
      />

      {/* ── bulk operations ── */}
      <BulkActionBar
        count={selectedIds.size}
        onMove={() => setBulkMoveOpen(true)}
        onTag={() => setBulkTagOpen(true)}
        onDownload={bulkDownload}
        onExport={exportSelected}
        onDelete={() => setBulkDeleteOpen(true)}
        onClear={clearSelection}
      />

      {bulkMoveOpen && (
        <MoveDocumentDialog
          open={bulkMoveOpen}
          onOpenChange={setBulkMoveOpen}
          documentId=""
          documentIds={[...selectedIds]}
          workspaceId={workspaceId}
          currentFolderId={currentFolderId ?? undefined}
          onDone={clearSelection}
        />
      )}

      <BulkTagDialog
        open={bulkTagOpen}
        onOpenChange={setBulkTagOpen}
        documents={selectedDocs.map((d) => ({ id: d.id, tags: d.tags }))}
        workspaceId={workspaceId}
        onDone={clearSelection}
      />

      <ConfirmDialog
        open={bulkDeleteOpen}
        onOpenChange={setBulkDeleteOpen}
        title={`Delete ${selectedIds.size} document${selectedIds.size === 1 ? '' : 's'}?`}
        description="The selected documents move to Trash, where an admin can restore them."
        confirmLabel="Delete"
        destructive
        loading={bulkBusy}
        onConfirm={bulkDelete}
      />

      {dragOver && (
        <div className="pointer-events-none absolute inset-0 z-40 flex items-center justify-center rounded-2xl bg-background/80 backdrop-blur-sm" aria-hidden>
          <div className="flex flex-col items-center gap-3 rounded-xl border-2 border-dashed border-primary bg-card px-10 py-8 text-center shadow-xl">
            <span className="flex h-12 w-12 items-center justify-center rounded-full bg-primary text-primary-foreground"><Upload className="h-6 w-6" /></span>
            <p className="text-base font-semibold">Drop to upload</p>
            <p className="max-w-xs text-sm text-muted-foreground">Files will be added to <strong className="text-foreground">{ws.data?.name ?? 'this workspace'}</strong>.</p>
          </div>
        </div>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/')({
  component: WorkspacePage,
  validateSearch: (raw: Record<string, unknown>): { doc?: string; folder?: string } => ({
    doc: typeof raw.doc === 'string' && raw.doc.length > 0 ? raw.doc : undefined,
    folder: typeof raw.folder === 'string' && raw.folder.length > 0 ? raw.folder : undefined,
  }),
})
