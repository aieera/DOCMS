import { useEffect, useMemo, useRef, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Upload, Settings as SettingsIcon, Search, FolderOpen, StickyNote } from 'lucide-react'

import { useDocumentsInfinite, documentsFromPages } from '@/hooks/useDocuments'
import { findRootFolder } from '@/lib/rootFolder'
import { useUpload } from '@/hooks/useUpload'
import { getFolder, getWorkspace, updateFolder } from '@/api/workspaces'
import { deleteDocument, getVersions, getDownloadURL, createNote, moveDocument } from '@/api/documents'
import { cn } from '@/lib/cn'
import { useCreateFolder, useFolders } from '@/hooks/useFolders'
import {
  DndContext, DragOverlay, PointerSensor, KeyboardSensor, useSensor, useSensors, pointerWithin,
  type DragStartEvent, type DragEndEvent,
} from '@dnd-kit/core'
import { DraggableTile, DroppableFolder, DragOverlayContent, type DragData, type DropData } from '@/components/folders/dnd'
import { readErrorMessage } from '@/api/client'

import { FolderBreadcrumbs } from '@/components/folders/FolderBreadcrumbs'
import { NewFolderDialog } from '@/components/folders/NewFolderDialog'
import { FolderActionsMenu } from '@/components/folders/FolderActionsMenu'
import { FolderTile, FileTile, NewFolderTile, FolderRow, FileRow, NewFolderRow } from '@/components/folders/BrowserTiles'
import { BrowserTreeSidebar } from '@/components/folders/BrowserTreeSidebar'
import { BrowserDetailsPanel, type Selection } from '@/components/folders/BrowserDetailsPanel'
import { DocumentActionsMenu } from '@/components/documents/DocumentActionsMenu'
import { DocumentViewerModal } from '@/components/documents/DocumentViewerModal'
import { MoveDocumentDialog } from '@/components/documents/MoveDocumentDialog'
import { BulkTagDialog } from '@/components/documents/BulkTagDialog'
import { BulkActionBar } from '@/components/documents/BulkActionBar'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { WorkspaceSettingsDialog } from '@/components/workspaces/WorkspaceSettingsDialog'
import { Button } from '@/components/ui/shadcn/button'
import { EmptyState } from '@/components/ui/EmptyState'
import { Input } from '@/components/ui/shadcn/input'
import { Skeleton } from '@/components/ui/Skeleton'
import { ViewModeToggle } from '@/components/ui/ViewModeToggle'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import { toast } from 'sonner'
import { runBatched } from '@/lib/runBatched'
import type { Document as ApiDocument } from '@/types/api'

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

  const ws = useQuery({
    queryKey: ['workspace', workspaceId],
    queryFn: () => getWorkspace(workspaceId),
    staleTime: 60_000,
    // A workspace that doesn't exist (or isn't ours) will not start
    // existing on a retry — retrying only multiplies the error toasts.
    retry: (failureCount, err) => !isMissingWorkspace(err) && failureCount < 2,
  })
  // BUG-07: the workspace query IS this page's identity, and its
  // failure used to be ignored entirely — /workspaces/<bogus-uuid>
  // rendered a complete, operable browser (breadcrumb "Workspace",
  // live New folder + Upload) whose every action then failed with a
  // raw "INVALID_ARGUMENT: required" toast. Failure is terminal for
  // the page, so nothing below should render.
  const wsFailure = workspaceFailureKind(ws.isError ? ws.error : null)
  const { data: foldersData, isLoading: foldersLoading } = useFolders(workspaceId, currentFolderId ?? undefined)
  const folderDetail = useQuery({ queryKey: ['folder', currentFolderId], queryFn: () => getFolder(currentFolderId!), enabled: !!currentFolderId })
  // At the workspace root, list the designated root folder's documents —
  // an unfiltered request returns EVERY document in the workspace, which
  // made files uploaded into any folder appear at the root too. The root
  // folder's own card is hidden below (it *is* the view), so its files
  // read as workspace-root files, Explorer-style.
  const rootFolder = !currentFolderId ? findRootFolder(foldersData ?? []) : undefined
  const effectiveFolderId = currentFolderId ?? rootFolder?.id
  const documentsParams: Record<string, string> = effectiveFolderId ? { folder_id: effectiveFolderId } : {}
  const { data, isLoading, fetchNextPage, hasNextPage, isFetchingNextPage } =
    useDocumentsInfinite(
      workspaceId,
      documentsParams,
      // Wait for the folder list before fetching at the root; firing the
      // unfiltered query first would flash the whole workspace's files.
      // A workspace with no root folder shows no root-level files at all
      // (documents cannot exist outside folders).
      // Also skip entirely once the workspace itself is known-bad —
      // it can only produce another copy of the same error toast.
      !wsFailure && (!!currentFolderId || (!foldersLoading && !!rootFolder)),
    )
  const { uploadFiles } = useUpload(workspaceId, effectiveFolderId ?? undefined)
  const qc = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const viewMode = useUIStore((s) => s.browserViewMode)
  const setViewMode = useUIStore((s) => s.setBrowserViewMode)
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
    () => documentsFromPages(data?.pages) as ApiDocument[],
    [data],
  )
  // Auto-load the next page when the sentinel scrolls into view.
  const loadMoreRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = loadMoreRef.current
    if (!el || !hasNextPage || isFetchingNextPage) return
    const io = new IntersectionObserver(
      (entries) => { if (entries.some((e) => e.isIntersecting)) fetchNextPage() },
      { rootMargin: '200px' },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [hasNextPage, isFetchingNextPage, fetchNextPage])

  const q = query.trim().toLowerCase()
  // The filter box matches client-side against loaded rows, so with a
  // cursor-paginated list it would silently miss anything not yet fetched.
  // While a filter is active, keep pulling pages so "no matches" means the
  // folder really has none.
  useEffect(() => {
    if (q && hasNextPage && !isFetchingNextPage) fetchNextPage()
  }, [q, hasNextPage, isFetchingNextPage, fetchNextPage])
  const visibleFolders = rootFolder ? folders.filter((f) => f.id !== rootFolder.id) : folders
  const shownFolders = q ? visibleFolders.filter((f) => f.name.toLowerCase().includes(q)) : visibleFolders
  const shownDocs = q ? docs.filter((d) => d.title.toLowerCase().includes(q)) : docs
  const usedBytes = useMemo(() => docs.reduce((sum, d) => sum + (Number(d.total_size_bytes) || 0), 0), [docs])
  // The details panel must reflect server state after a mutation (e.g. the
  // share/private toggle). `selection` captures the row object at click
  // time, so after an invalidation the panel would keep rendering the stale
  // snapshot; re-derive the current object from the fresh lists by id,
  // falling back to the snapshot while it's mid-refetch or filtered out.
  const freshSelection: Selection = useMemo(() => {
    if (!selection) return null
    if (selection.type === 'folder') {
      const f = folders.find((x) => x.id === selection.folder.id)
      return f ? { type: 'folder', folder: f } : selection
    }
    const d = docs.find((x) => x.id === selection.doc.id)
    return d ? { type: 'file', doc: d } : selection
  }, [selection, folders, docs])

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

  // Prune the multi-select to documents that still exist whenever the
  // list refetches. Per-row actions (delete, move-out-of-folder via the
  // actions menu or drag-and-drop) remove a doc from `docs` but left its
  // id in `selectedIds`, inflating the bulk count and re-submitting a
  // stale id to the next bulk op. Return the previous set unchanged when
  // nothing was pruned so this doesn't trigger an extra render.
  useEffect(() => {
    const alive = new Set(docs.map((d) => d.id))
    setSelectedIds((prev) => {
      let changed = false
      const next = new Set<string>()
      for (const id of prev) {
        if (alive.has(id)) next.add(id)
        else changed = true
      }
      return changed ? next : prev
    })
    setAnchorId((prev) => (prev && !alive.has(prev) ? null : prev))
  }, [docs])

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
    // Bare ['documents'] — must also hit ['documents', 'infinite', …],
    // which a ['documents', workspaceId] prefix does not match.
    qc.invalidateQueries({ queryKey: ['documents'] })
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

  // ── upload — no enrichment prompt. Tags and classification arrive
  // automatically from the intelligence pipeline after OCR/NER (the
  // auto_tag task applies candidates ≥ the tenant's auto-apply
  // threshold; mid-confidence ones surface as pending suggestions). ──
  const startUpload = (files: File[]) => {
    if (!files.length) return
    uploadFiles(files)
  }

  // New note: create a note document in the current folder, then open the
  // collaborative editor on it (the ?doctype hint mounts the editor before
  // the first markdown version exists). Notes need a folder to live in.
  const onNewNote = async () => {
    // effectiveFolderId, not currentFolderId: at the workspace root the
    // note belongs in the root folder, exactly like an upload there.
    if (!effectiveFolderId) return
    const note = await createNote({ workspace_id: workspaceId, folder_id: effectiveFolderId, doc_type: 'note' })
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

  // Click-vs-double-click: selecting mounts the details panel, which
  // reflows the tile grid — the tile literally moves between the two
  // clicks of a double-click, so the second click misses and open
  // never fires. Defer the single-click selection just past the
  // double-click window; an open cancels the pending selection.
  const selectTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(selectTimer.current), [])
  const scheduleSelect = (fn: () => void) => {
    clearTimeout(selectTimer.current)
    selectTimer.current = setTimeout(fn, 220)
  }
  const openNow = (fn: () => void) => {
    clearTimeout(selectTimer.current)
    fn()
  }

  // Terminal states. Placed after every hook so the hook order never
  // depends on the query outcome.
  if (wsFailure) return <WorkspaceUnavailable kind={wsFailure} />

  const loadingTiles = (n: number) =>
    Array.from({ length: n }).map((_, i) => (
      <Skeleton key={i} className={viewMode === 'grid' ? 'h-[152px] rounded-2xl' : 'h-11 rounded-xl'} />
    ))
  const nothingHere = !foldersLoading && !isLoading && shownFolders.length === 0 && shownDocs.length === 0
  // Grid mode: responsive tile wall. List mode: single-column rows.
  const sectionClass = viewMode === 'grid'
    ? 'grid grid-cols-[repeat(auto-fill,minmax(162px,1fr))] gap-4'
    : 'flex flex-col gap-1'
  // Explorer-style: clicking the empty background (not a tile/row)
  // deselects, which also dismisses the details panel.
  const clearOnBackgroundClick = (e: React.MouseEvent) => {
    if (e.target !== e.currentTarget) return
    setSelection(null)
    setSelectedIds(new Set())
  }
  // In list mode the ⋯ trigger moves from the tile's top-end corner into
  // the row's trailing gutter (vertically centered).
  const menuTriggerClass = viewMode === 'grid' ? undefined : 'absolute end-1 top-1/2 -translate-y-1/2'
  // With 2+ docs checked, per-file menus disappear — the bulk bar is the
  // single action surface for a multi-selection.
  const multiSelect = selectedIds.size >= 2

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
          <div className="flex flex-none items-center gap-2">
            <ViewModeToggle value={viewMode} onChange={setViewMode} />
            {isAdmin && (
              <Button variant="ghost" size="sm" onClick={() => setSettingsOpen(true)} className="gap-2" data-testid="open-ws-settings">
                <SettingsIcon className="h-4 w-4" /> Settings
              </Button>
            )}
          </div>
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
            disabled={!effectiveFolderId}
            variant="outline"
            className="h-12 gap-2 rounded-xl px-5"
            // A title tooltip is invisible to keyboard and touch users, so
            // the reason is announced instead of merely hovered.
            aria-describedby={!effectiveFolderId ? 'new-note-hint' : undefined}
            data-testid="new-note"
          >
            <StickyNote className="h-[18px] w-[18px]" /> New note
          </Button>
          {!effectiveFolderId && (
            <p id="new-note-hint" className="sr-only">
              Still loading this workspace&rsquo;s folders.
            </p>
          )}
          <Button onClick={onPick} className="h-12 gap-2 rounded-xl px-5 shadow-sm hover:shadow-md" data-testid="open-upload">
            <Upload className="h-[18px] w-[18px]" /> Upload
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-10 pt-5" onClick={clearOnBackgroundClick}>
          <h2 className="mb-4 px-0.5 text-lg font-bold tracking-tight text-foreground">Folders</h2>
          <div className={sectionClass} onClick={clearOnBackgroundClick}>
            {foldersLoading ? loadingTiles(4) : (
              <>
                {shownFolders.filter((f) => !movingIds.has(f.id)).map((f) => {
                  const FolderItem = viewMode === 'grid' ? FolderTile : FolderRow
                  return (
                    <DroppableFolder key={f.id} folderId={f.id}>
                      <DraggableTile dndId={`folder:${f.id}`} data={{ kind: 'folder', id: f.id, label: f.name }}>
                        <FolderActionsMenu folder={f} canManage={isAdmin} onOpen={() => navigateToFolder(f.id)} triggerClassName={menuTriggerClass}>
                          <FolderItem
                            folder={f}
                            selected={selection?.type === 'folder' && selection.folder.id === f.id}
                            onSelect={() => scheduleSelect(() => setSelection({ type: 'folder', folder: f }))}
                            onOpen={() => openNow(() => navigateToFolder(f.id))}
                          />
                        </FolderActionsMenu>
                      </DraggableTile>
                    </DroppableFolder>
                  )
                })}
                {!q && (viewMode === 'grid'
                  ? <NewFolderTile onClick={() => setNewFolderOpen(true)} />
                  : <NewFolderRow onClick={() => setNewFolderOpen(true)} />)}
              </>
            )}
          </div>

          <h2 className="mb-4 mt-9 px-0.5 text-lg font-bold tracking-tight text-foreground">Files</h2>
          <div className={sectionClass} onClick={clearOnBackgroundClick}>
            {isLoading ? loadingTiles(4) : shownDocs.filter((d) => !movingIds.has(d.id)).map((d) => {
              const checked = selectedIds.has(d.id)
              const FileItem = viewMode === 'grid' ? FileTile : FileRow
              return (
                <div key={d.id} className="group/sel relative">
                  {/* Accessible multi-select checkbox, overlaid top-start
                      (grid) or in the row's start gutter (list). Visible on
                      hover/focus, or whenever a selection is active.
                      stopPropagation so toggling doesn't open the doc or
                      change the details-panel selection. */}
                  <div
                    className={cn(
                      'absolute z-10 transition-opacity',
                      // List rows: stretch the gutter full-height and let
                      // flex center the box — no pixel math to drift.
                      viewMode === 'grid' ? 'start-2 top-2' : 'inset-y-0 start-3 flex items-center',
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
                    {(() => {
                      const item = (
                        <FileItem
                          doc={d}
                          selected={checked || (selection?.type === 'file' && selection.doc.id === d.id)}
                          onSelect={() => scheduleSelect(() => setSelection({ type: 'file', doc: d }))}
                          onOpen={() => openNow(() => openDoc(d.id))}
                        />
                      )
                      return multiSelect ? item : (
                        <DocumentActionsMenu doc={d} onOpen={() => openDoc(d.id)} triggerClassName={menuTriggerClass}>
                          {item}
                        </DocumentActionsMenu>
                      )
                    })()}
                  </DraggableTile>
                </div>
              )
            })}
            {!isLoading && shownDocs.length === 0 && !nothingHere && (
              <p className="col-span-full py-6 text-sm text-muted-foreground">No files here yet.</p>
            )}
          </div>

          {/* Cursor pagination. The sentinel auto-loads on scroll; the button
              is the accessible, no-observer fallback and also tells the user
              that more exists — without it a folder just looked 20 files
              long. */}
          {hasNextPage && (
            <div ref={loadMoreRef} className="mt-4 flex justify-center">
              <Button
                variant="outline"
                size="sm"
                disabled={isFetchingNextPage}
                onClick={() => fetchNextPage()}
              >
                {isFetchingNextPage ? 'Loading…' : 'Load more files'}
              </Button>
            </div>
          )}

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

      {/* ── right: details (only while something is selected) ── */}
      <BrowserDetailsPanel
        selection={freshSelection}
        workspace={ws.data}
        onOpenFolder={(f) => navigateToFolder(f.id)}
        onOpenFile={(d) => openDoc(d.id)}
        onClose={() => setSelection(null)}
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

      <DocumentViewerModal
        open={!!search.doc}
        onOpenChange={(o) => { if (!o) closeViewer() }}
        documentId={search.doc ?? null}
        workspaceId={workspaceId}
      />

      {/* ── bulk operations — only for a real multi-selection. A single
          checkbox tick is a quiet select (its actions live in the row's
          ⋯ menu), matching the Explorer-style selection model. ── */}
      {selectedIds.size >= 2 && (
        <BulkActionBar
          count={selectedIds.size}
          onMove={() => setBulkMoveOpen(true)}
          onTag={() => setBulkTagOpen(true)}
          onDownload={bulkDownload}
          onExport={exportSelected}
          onDelete={() => setBulkDeleteOpen(true)}
          onClear={clearSelection}
        />
      )}

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

// ---- workspace-unavailable handling (BUG-07) -----------------------

export type WorkspaceFailureKind = 'not-found' | 'forbidden'

/** 400 covers the invalid-uuid case: the backend rejects a malformed
 *  workspace id with INVALID_ARGUMENT long before it looks anything up,
 *  which for a URL the user opened means exactly "no such workspace". */
function isMissingWorkspace(err: unknown): boolean {
  const status = (err as { response?: { status?: number } } | null)?.response?.status
  return status === 400 || status === 403 || status === 404
}

export function workspaceFailureKind(err: unknown): WorkspaceFailureKind | null {
  if (!err) return null
  const status = (err as { response?: { status?: number } }).response?.status
  if (status === 403) return 'forbidden'
  if (status === 400 || status === 404) return 'not-found'
  // Anything else (5xx, network) is transient — keep rendering the page
  // so its own retry/refetch paths still apply.
  return null
}

/** Replaces the whole browser when the workspace can't be opened, so
 *  there is no New folder / Upload / New note affordance left to click.
 *  Reuses EmptyState (the app's standard nothing-here surface) rather
 *  than inventing a second 404 look. */
export function WorkspaceUnavailable({ kind }: { kind: WorkspaceFailureKind }) {
  const forbidden = kind === 'forbidden'
  return (
    <div
      className="flex min-h-0 flex-1 items-center justify-center rounded-2xl border border-border bg-card shadow-sm"
      data-testid={forbidden ? 'workspace-forbidden' : 'workspace-not-found'}
    >
      <EmptyState
        icon={<FolderOpen className="h-6 w-6" />}
        title={forbidden ? "You don't have access to this workspace" : 'Workspace not found'}
        description={
          forbidden
            ? 'Ask a workspace owner or an administrator to add you, then reopen this link.'
            : "This workspace doesn't exist, or it was deleted. The link may be stale or mistyped."
        }
        action={
          <Button asChild>
            <Link to="/workspaces">Back to workspaces</Link>
          </Button>
        }
      />
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
