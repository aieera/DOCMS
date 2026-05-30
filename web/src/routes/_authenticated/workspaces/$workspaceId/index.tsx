import { useRef, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Upload, Sparkles, FolderOpen, FileText, Users, FolderPlus } from 'lucide-react'

import { useDocuments } from '@/hooks/useDocuments'
import { useUpload } from '@/hooks/useUpload'
import { getFolder, getWorkspace } from '@/api/workspaces'
import { updateDocument } from '@/api/documents'
import { useCreateFolder, useDeleteFolder, useFolders, useRenameFolder } from '@/hooks/useFolders'
import { FolderBreadcrumbs } from '@/components/folders/FolderBreadcrumbs'
import { FolderGrid } from '@/components/folders/FolderGrid'
import { NewFolderDialog } from '@/components/folders/NewFolderDialog'
import { readErrorMessage } from '@/api/client'
import {
  predictFiling,
  sendFilingFeedback,
  type PredictResponse,
} from '@/api/predictiveFiling'
import { DocumentList } from '@/components/documents/DocumentList'
import { DocumentViewerModal } from '@/components/documents/DocumentViewerModal'
import { useNavigate } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Link } from '@tanstack/react-router'
import { Settings as SettingsIcon } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
import { WorkspaceAISettingsDialog } from '@/components/intelligence/WorkspaceAISettings'
import {
  UploadEnrichmentDialog,
  type EnrichmentDecision,
} from '@/components/documents/UploadEnrichmentDialog'
import { toast } from 'sonner'
import type { Document as ApiDocument } from '@/types/api'

// Post-upload enrichment queue item. Populated by useUpload's
// onComplete hook once createVersion succeeds AND the parallel
// prediction promise resolves. Dialog dequeues one at a time so
// multi-file uploads still surface a single modal at a time.
interface EnrichmentItem {
  file: File
  docId: string
  prediction: PredictResponse | null
}

function WorkspacePage() {
  const { workspaceId } = Route.useParams()
  // ?doc=<uuid> drives the document viewer modal.
  // ?folder=<uuid> drives the current-folder navigation — empty/missing
  //   means the workspace root. Storing both in the URL makes deep
  //   links to a specific folder bookmarkable + back/forward natural.
  const search = Route.useSearch() as { doc?: string; folder?: string }
  const navigate = useNavigate({ from: '/workspaces/$workspaceId/' })
  const { t } = useTranslation('folders')
  const currentFolderId = search.folder ?? null
  const closeViewer = () =>
    navigate({ search: (s: Record<string, unknown>) => ({ ...s, doc: undefined }) })
  const navigateToFolder = (folderId: string | null) =>
    navigate({
      search: (s: Record<string, unknown>) => ({ ...s, folder: folderId ?? undefined }),
    })
  const ws = useQuery({
    queryKey: ['workspace', workspaceId],
    queryFn: () => getWorkspace(workspaceId),
    staleTime: 60_000,
  })
  // Documents in the current folder only. When at workspace root we
  // pass the root folder id once it's been discovered via useFolders;
  // the backend's ListDocuments accepts folder_id as a filter.
  const { data: foldersData, isLoading: foldersLoading } = useFolders(
    workspaceId,
    currentFolderId ?? undefined,
  )
  // Folder detail for breadcrumbs — only fetch when a folder is selected.
  const folderDetail = useQuery({
    queryKey: ['folder', currentFolderId],
    queryFn: () => getFolder(currentFolderId!),
    enabled: !!currentFolderId,
  })
  const documentsParams: Record<string, string> = currentFolderId
    ? { folder_id: currentFolderId }
    : {}
  const { data, isLoading } = useDocuments(workspaceId, documentsParams)
  const { uploadFiles } = useUpload(workspaceId, currentFolderId ?? undefined)
  const qc = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [aiOpen, setAiOpen] = useState(false)
  const [newFolderOpen, setNewFolderOpen] = useState(false)
  const [dragOver, setDragOver] = useState(false)
  const createFolder = useCreateFolder()
  const renameFolder = useRenameFolder()
  const deleteFolder = useDeleteFolder()
  // dragCounter pattern: native dragenter/dragleave fire as the cursor
  // crosses every CHILD element under the drop zone, so a naive
  // boolean toggles in and out as the user drags over interior
  // content (cards, badges, etc.), producing visible overlay
  // flicker. Counting enter/leave events instead — and only
  // toggling the overlay at 0↔1 transitions — keeps it stable.
  const dragCounter = useRef(0)

  const role = useAuthStore((s) => s.user?.role)
  // Workspace AI settings are admin-curated; matches the policy
  // service's owner|admin gate on PUT. Hide for non-admins so they
  // don't see a button that always 403s.
  const isAdmin = role === 'owner' || role === 'admin'

  // Post-upload enrichment queue. The dialog renders queue[0]; on
  // confirm/skip we shift the head, so multi-file batches surface
  // one modal at a time without any global state.
  const [enrichmentQueue, setEnrichmentQueue] = useState<EnrichmentItem[]>([])
  // Prediction promises keyed by file identity. We fire predict for
  // every picked file in parallel at pick-time, store the promise
  // here, and consume it in the onComplete callback so the dialog
  // can open the moment BOTH the upload and the prediction have
  // settled. A failed prediction resolves to null rather than
  // rejecting — graceful degradation per the spec.
  const predictionsRef = useRef<Map<File, Promise<PredictResponse | null>>>(new Map())

  const startUpload = (files: File[]) => {
    if (!files.length) return
    // 1. Kick off predictions in parallel with the uploads. predictFiling
    //    is a single POST; firing N of them at once is fine. A failed
    //    prediction resolves to null so the dialog still surfaces.
    for (const file of files) {
      const p = predictFiling({
        filename: file.name,
        mime_type: file.type || 'application/octet-stream',
        workspace_id: workspaceId,
      }).catch(() => null)
      predictionsRef.current.set(file, p)
    }
    // 2. Hand the batch to useUpload. For every successful per-file
    //    upload it fires onComplete; we await the matching prediction
    //    and enqueue the (file, docId, prediction) triple. useUpload
    //    does NOT await onComplete, so file N+1 starts uploading
    //    while we wait for file N's prediction in the background.
    uploadFiles(files, undefined, async (file, docId) => {
      const pred = (await predictionsRef.current.get(file)) ?? null
      predictionsRef.current.delete(file)
      setEnrichmentQueue((q) => [...q, { file, docId, prediction: pred }])
    })
  }

  const dequeueEnrichment = () =>
    setEnrichmentQueue((q) => q.slice(1))

  const handleEnrichmentConfirm = async (item: EnrichmentItem, decision: EnrichmentDecision) => {
    dequeueEnrichment()
    try {
      await updateDocument(item.docId, {
        title: decision.title,
        document_class: decision.documentClass,
        tags: decision.tags,
      })
      await qc.invalidateQueries({ queryKey: ['documents', workspaceId] })
    } catch (e) {
      toast.error(`Couldn't update ${item.file.name}: ${(e as Error).message}`)
    }
    // Feedback rides on a prediction_id, so we only send it when
    // there was an actual prediction. Best-effort; UI doesn't block
    // on training-corpus writes.
    if (item.prediction) {
      const p = item.prediction
      const predictedTags = p.suggested_tags.map((t) => t.tag)
      const tagsAccepted = decision.tags.filter((t) => predictedTags.includes(t))
      const tagsRejected = predictedTags.filter((t) => !decision.tags.includes(t))
      sendFilingFeedback({
        prediction_id:          p.prediction_id,
        class_accepted:         decision.documentClass === p.classification.predicted_class,
        folder_accepted:        false,
        tags_accepted:          tagsAccepted,
        tags_rejected:          tagsRejected,
        final_class:            decision.documentClass,
        final_tags:             decision.tags,
        predicted_class:        p.classification.predicted_class,
        predicted_class_score:  p.classification.confidence,
        predicted_folder_id:    p.suggested_folder?.id,
        predicted_folder_score: p.suggested_folder?.score ?? 0,
        predicted_tags:         predictedTags,
        filename:               item.file.name,
        mime_type:              item.file.type || 'application/octet-stream',
        workspace_id:           workspaceId,
      }).catch((e) => console.warn('filing feedback failed', e))
    }
  }

  const handleEnrichmentSkip = (item: EnrichmentItem) => {
    dequeueEnrichment()
    if (!item.prediction) return
    const p = item.prediction
    sendFilingFeedback({
      prediction_id:          p.prediction_id,
      class_accepted:         false,
      folder_accepted:        false,
      tags_accepted:          [],
      tags_rejected:          p.suggested_tags.map((t) => t.tag),
      final_class:            p.classification.predicted_class,
      final_tags:             [],
      predicted_class:        p.classification.predicted_class,
      predicted_class_score:  p.classification.confidence,
      predicted_folder_id:    p.suggested_folder?.id,
      predicted_folder_score: p.suggested_folder?.score ?? 0,
      predicted_tags:         p.suggested_tags.map((t) => t.tag),
      filename:               item.file.name,
      mime_type:               item.file.type || 'application/octet-stream',
      workspace_id:           workspaceId,
    }).catch((e) => console.warn('filing feedback failed', e))
  }

  const onPick = () => fileInputRef.current?.click()
  const onChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files
    if (!files || files.length === 0) return
    startUpload(Array.from(files))
    e.target.value = ''
  }

  const onDrop = (e: React.DragEvent) => {
    e.preventDefault()
    dragCounter.current = 0
    setDragOver(false)
    const files = Array.from(e.dataTransfer.files ?? [])
    if (files.length) startUpload(files)
  }

  return (
    <div
      onDragEnter={(e) => {
        e.preventDefault()
        // Only react to file drags — ignore text/range/etc selections
        // that produce dragenter on the document body during ordinary
        // mouse use.
        if (!Array.from(e.dataTransfer?.types ?? []).includes('Files')) return
        dragCounter.current += 1
        if (dragCounter.current === 1) setDragOver(true)
      }}
      onDragOver={(e) => { e.preventDefault() }}
      onDragLeave={(e) => {
        e.preventDefault()
        dragCounter.current = Math.max(0, dragCounter.current - 1)
        if (dragCounter.current === 0) setDragOver(false)
      }}
      onDrop={onDrop}
      className="relative space-y-6"
    >
      <input ref={fileInputRef} type="file" multiple className="hidden" onChange={onChange} />

      <PageHeader
        title={
          ws.isLoading ? (
            <Skeleton className="h-8 w-48" />
          ) : (
            <span className="flex items-center gap-3">
              <span className="flex h-10 w-10 items-center justify-center rounded-lg bg-muted text-foreground">
                <FolderOpen className="h-5 w-5" />
              </span>
              <span className="text-2xl font-semibold tracking-tight">{ws.data?.name ?? 'Workspace'}</span>
            </span>
          )
        }
        description={
          ws.data ? (
            <span className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
              {ws.data.description && (
                <>
                  <span>{ws.data.description}</span>
                  <span aria-hidden className="text-muted-foreground/40">•</span>
                </>
              )}
              <span className="flex items-center gap-1">
                <FileText className="h-3.5 w-3.5" />
                {ws.data.document_count.toLocaleString()} {ws.data.document_count === 1 ? 'document' : 'documents'}
              </span>
              <span aria-hidden className="text-muted-foreground/40">•</span>
              <span className="flex items-center gap-1">
                <Users className="h-3.5 w-3.5" />
                {ws.data.member_count ?? 0} {(ws.data.member_count ?? 0) === 1 ? 'member' : 'members'}
              </span>
            </span>
          ) : ws.isLoading ? <Skeleton className="h-4 w-72" /> : undefined
        }
        actions={
          <>
            {isAdmin && (
              <Button variant="outline" onClick={() => setAiOpen(true)} data-testid="open-ai-settings" className="gap-2">
                <Sparkles className="h-4 w-4" /> AI settings
              </Button>
            )}
            {isAdmin && (
              <Button variant="outline" asChild data-testid="open-ws-settings" className="gap-2">
                <Link
                  to="/workspaces/$workspaceId/settings"
                  params={{ workspaceId }}
                  aria-label="Workspace settings"
                >
                  <SettingsIcon className="h-4 w-4" /> Settings
                </Link>
              </Button>
            )}
            <Button
              variant="outline"
              onClick={() => setNewFolderOpen(true)}
              data-testid="open-new-folder"
              className="gap-2"
            >
              <FolderPlus className="h-4 w-4" />
              {t('toolbar.new_folder')}
            </Button>
            <Button onClick={onPick} data-testid="open-upload" className="gap-2 shadow-sm hover:shadow-md">
              <Upload className="h-4 w-4" /> Upload
            </Button>
          </>
        }
      />

      {/* Folder breadcrumb + nested folders. Rendered between the
          header and document list so navigation always reads
          "workspace > folder > current" before the contents. */}
      <FolderBreadcrumbs
        ancestors={folderDetail.data?.ancestors ?? []}
        current={folderDetail.data ?? null}
        workspaceName={ws.data?.name ?? ''}
        onNavigate={navigateToFolder}
      />

      <FolderGrid
        folders={foldersData ?? []}
        isLoading={foldersLoading}
        onOpen={(f) => navigateToFolder(f.id)}
        onRename={(folder, name) => {
          renameFolder.mutate(
            { folderId: folder.id, name },
            {
              onSuccess: () => {
                toast.success(t('toasts.renamed'))
                qc.invalidateQueries({ queryKey: ['folder', folder.id] })
              },
            },
          )
        }}
        onDelete={(folder) => {
          deleteFolder.mutate(folder.id, {
            onSuccess: () => toast.success(t('toasts.deleted')),
            onError: (e: unknown) => {
              const msg = readErrorMessage(e) ?? ''
              toast.error(msg.includes('not empty') ? t('toasts.delete_not_empty') : (msg || t('toasts.error')))
            },
          })
        }}
        canManage={isAdmin}
      />

      {/* Backend ListDocumentsResponse uses `documents`, not `items` —
          the PaginatedResponse<T> type's `items` field is wrong for
          this endpoint. Read both for resilience. */}
      <DocumentList
        documents={((data as unknown as { documents?: unknown[]; items?: unknown[] })?.documents
          ?? (data as unknown as { items?: unknown[] })?.items
          ?? []) as ApiDocument[]}
        isLoading={isLoading}
      />

      <WorkspaceAISettingsDialog open={aiOpen} onOpenChange={setAiOpen} workspaceId={workspaceId} />

      <NewFolderDialog
        open={newFolderOpen}
        onOpenChange={setNewFolderOpen}
        parentName={folderDetail.data?.name ?? null}
        canPickVisibility={false}
        isCreating={createFolder.isPending}
        onCreate={async (name) => {
          await new Promise<void>((resolve, reject) => {
            createFolder.mutate(
              { workspaceId, name, parentId: currentFolderId ?? undefined },
              {
                onSuccess: () => {
                  toast.success(t('toasts.created'))
                  setNewFolderOpen(false)
                  resolve()
                },
                onError: (e) => {
                  toast.error(readErrorMessage(e) ?? t('toasts.error'))
                  reject(e as Error)
                },
              },
            )
          })
        }}
      />

      {/* Post-upload enrichment. Renders queue[0] only; keying on
          docId forces a fresh useState on dequeue so the next file's
          prediction lands cleanly in the form state. */}
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

      {/* Document viewer modal — opens when ?doc=<id> is present. The
          modal hosts the FULL document detail surface (all 9 tabs +
          sidebar + every dialog the page can spawn). Close clears the
          search param. Direct-URL navigation to /workspaces/.../
          documents/<id> still renders the full page unchanged. */}
      <DocumentViewerModal
        open={!!search.doc}
        onOpenChange={(o) => { if (!o) closeViewer() }}
        documentId={search.doc ?? null}
        workspaceId={workspaceId}
      />

      {/* Full-page drop overlay shown only while a drag is active.
          The visual matches the upload button so users connect the
          dots: dropping anywhere → same flow as clicking Upload. */}
      {dragOver && (
        <div
          className="pointer-events-none fixed inset-0 z-40 flex items-center justify-center bg-background/80 backdrop-blur-sm"
          aria-hidden
        >
          <div className="flex flex-col items-center gap-3 rounded-xl border-2 border-dashed border-foreground bg-card px-10 py-8 text-center shadow-xl">
            <span className="flex h-12 w-12 items-center justify-center rounded-full bg-foreground text-background">
              <Upload className="h-6 w-6" />
            </span>
            <p className="text-base font-semibold">Drop to upload</p>
            <p className="max-w-xs text-sm text-muted-foreground">
              Files will be added to <strong className="text-foreground">{ws.data?.name ?? 'this workspace'}</strong>.
            </p>
          </div>
        </div>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/')({
  component: WorkspacePage,
  // ?doc=<uuid> opens DocumentViewerModal as an overlay; URL-driven so
  // back-button and bookmarks behave naturally. Anything else in search
  // passes through (e.g. future ?folder= or ?filter= params).
  validateSearch: (raw: Record<string, unknown>): { doc?: string; folder?: string } => ({
    doc: typeof raw.doc === 'string' && raw.doc.length > 0 ? raw.doc : undefined,
    folder: typeof raw.folder === 'string' && raw.folder.length > 0 ? raw.folder : undefined,
  }),
})
