import { useMemo, useRef, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Upload, Settings as SettingsIcon, Search, FolderOpen } from 'lucide-react'

import { useDocuments } from '@/hooks/useDocuments'
import { useUpload } from '@/hooks/useUpload'
import { getFolder, getWorkspace } from '@/api/workspaces'
import { updateDocument } from '@/api/documents'
import { useCreateFolder, useFolders } from '@/hooks/useFolders'
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
import { WorkspaceSettingsDialog } from '@/components/workspaces/WorkspaceSettingsDialog'
import { UploadEnrichmentDialog, type EnrichmentDecision } from '@/components/documents/UploadEnrichmentDialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Skeleton } from '@/components/ui/Skeleton'
import { useAuthStore } from '@/store/authStore'
import { toast } from 'sonner'
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
          <Button onClick={onPick} className="h-12 gap-2 rounded-xl px-5 shadow-sm hover:shadow-md" data-testid="open-upload">
            <Upload className="h-[18px] w-[18px]" /> Upload
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-10 pt-5">
          <h2 className="mb-4 px-0.5 text-lg font-bold tracking-tight text-foreground">Folders</h2>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(162px,1fr))] gap-4">
            {foldersLoading ? loadingTiles(4) : (
              <>
                {shownFolders.map((f) => (
                  <FolderActionsMenu key={f.id} folder={f} canManage={isAdmin} onOpen={() => navigateToFolder(f.id)}>
                    <FolderTile
                      folder={f}
                      selected={selection?.type === 'folder' && selection.folder.id === f.id}
                      onSelect={() => setSelection({ type: 'folder', folder: f })}
                      onOpen={() => navigateToFolder(f.id)}
                    />
                  </FolderActionsMenu>
                ))}
                {!q && <NewFolderTile onClick={() => setNewFolderOpen(true)} />}
              </>
            )}
          </div>

          <h2 className="mb-4 mt-9 px-0.5 text-lg font-bold tracking-tight text-foreground">Files</h2>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(162px,1fr))] gap-4">
            {isLoading ? loadingTiles(4) : shownDocs.map((d) => (
              <DocumentActionsMenu key={d.id} doc={d}>
                <FileTile
                  doc={d}
                  selected={selection?.type === 'file' && selection.doc.id === d.id}
                  onSelect={() => setSelection({ type: 'file', doc: d })}
                  onOpen={() => openDoc(d.id)}
                />
              </DocumentActionsMenu>
            ))}
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
