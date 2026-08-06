import { toast } from 'sonner'
import { useQuery } from '@tanstack/react-query'
import { FolderOpen, X } from 'lucide-react'

import { cn } from '@/lib/cn'
import { formatFileSize, formatDateTime, formatRelativeTime } from '@/lib/formatters'
import { Button } from '@/components/ui/shadcn/button'
import { useSetFolderVisibility } from '@/hooks/useFolders'
import { useActivityForDocument } from '@/hooks/useDocumentDetailGQL'
import { useAuthStore } from '@/store/authStore'
import { getAuditLog } from '@/api/admin'
import { readErrorMessage } from '@/api/client'
import { FolderGlyph, FileTypeIcon, fileKind } from '@/components/folders/BrowserTiles'
import type { Document, Folder, Workspace } from '@/types/api'

export type Selection =
  | { type: 'folder'; folder: Folder }
  | { type: 'file'; doc: Document }
  | null

interface Props {
  selection: Selection
  workspace?: Workspace
  onOpenFolder: (folder: Folder) => void
  onOpenFile: (doc: Document) => void
  onClose: () => void
}

// Windows-Explorer-style details pane: only present while a file or
// folder is selected; deselecting (background click, folder change, or
// the ✕ here) removes it entirely.
export function BrowserDetailsPanel({ selection, workspace, onOpenFolder, onOpenFile, onClose }: Props) {
  if (selection === null) return null
  return (
    // Flex column inside the page's bounded height: the ✕ header stays
    // put and only the details body scrolls — no viewport math, so it
    // holds together at any zoom level / window size.
    <aside className="hidden min-h-0 w-[300px] flex-none flex-col border-s border-border xl:flex" data-testid="details-panel">
      <div className="flex shrink-0 justify-end px-3 pt-3">
        <button
          type="button"
          onClick={onClose}
          aria-label="Close details"
          title="Close details"
          className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <X className="h-4 w-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-6 pt-0">
        {selection.type === 'folder' ? (
          <FolderDetails folder={selection.folder} workspace={workspace} onOpen={() => onOpenFolder(selection.folder)} />
        ) : (
          <FileDetails doc={selection.doc} workspace={workspace} onOpen={() => onOpenFile(selection.doc)} />
        )}
      </div>
    </aside>
  )
}

function FolderDetails({ folder, workspace, onOpen }: { folder: Folder; workspace?: Workspace; onOpen: () => void }) {
  const setVis = useSetFolderVisibility()
  const childCount = Number(folder.child_folder_count ?? folder.children_count ?? 0) || 0
  const items = (Number(folder.document_count) || 0) + childCount
  const isShared = folder.visibility !== 'private'

  const toggle = () =>
    setVis.mutate(
      { folderId: folder.id, visibility: isShared ? 'private' : 'shared' },
      {
        onSuccess: () => toast.success(isShared ? 'Folder is now private' : 'Folder is now shared'),
        onError: (e) => toast.error(readErrorMessage(e) ?? 'Could not change visibility'),
      },
    )

  return (
    <div>
      <Preview><FolderGlyph size={108} /></Preview>
      <Name>{folder.name}</Name>

      <Head>Info</Head>
      <div className="space-y-3.5">
        <Row k="Type" v="Folder" />
        <Row k="Contents" v={`${items} ${items === 1 ? 'item' : 'items'}`} />
        <Row k="Location" v={workspace?.name ?? '—'} link />
        <Row k="Modified" v={formatDateTime(folder.updated_at ?? folder.created_at)} />
        <Row k="Created" v={formatDateTime(folder.created_at)} />
      </div>

      <Divider />
      <Head>Settings</Head>
      <div className="flex items-center justify-between py-1.5">
        <div>
          <div className="text-sm font-semibold text-foreground">Shared with workspace</div>
          <div className="text-xs text-muted-foreground">{isShared ? 'Everyone in the workspace can see it' : 'Only people with explicit access'}</div>
        </div>
        <Switch checked={isShared} disabled={setVis.isPending} onChange={toggle} />
      </div>

      <FolderActivity folderId={folder.id} />

      <Button variant="outline" className="mt-5 w-full gap-2" onClick={onOpen}>
        <FolderOpen className="h-4 w-4" /> Open folder
      </Button>
    </div>
  )
}

// ---- Recent activity ------------------------------------------------------

interface ActivityRow {
  id: string
  summary: string
  actor?: string
  at: string
}

function ActivitySection({ rows, isLoading }: { rows: ActivityRow[]; isLoading: boolean }) {
  return (
    <>
      <Divider />
      <Head>Recent activity</Head>
      {isLoading ? (
        <div className="space-y-2.5" aria-hidden>
          {[0, 1, 2].map((i) => (
            <div key={i} className="h-3.5 animate-pulse rounded bg-muted/60" />
          ))}
        </div>
      ) : rows.length === 0 ? (
        <p className="text-xs text-muted-foreground">No recent activity</p>
      ) : (
        <ul className="space-y-2.5">
          {rows.map((r) => (
            <li key={r.id} className="text-xs leading-snug">
              <span className="text-foreground">{r.summary}</span>
              <span className="text-muted-foreground">
                {' — '}
                {r.actor ? `${r.actor}, ` : ''}
                {formatRelativeTime(r.at)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

// Folder activity comes from the audit service, whose events endpoint is
// admin/owner-gated server-side — render nothing for other roles rather
// than farming 403s. Files use the per-document activity resolver instead,
// which is permission-checked for any reader.
function FolderActivity({ folderId }: { folderId: string }) {
  const role = useAuthStore((st) => st.user?.role)
  const canReadAudit = role === 'admin' || role === 'owner'
  const q = useQuery({
    queryKey: ['folder-activity', folderId],
    enabled: canReadAudit,
    queryFn: () =>
      getAuditLog({ resource_type: 'folder', resource_id: folderId, page_size: '5' }),
  })
  if (!canReadAudit || q.isError) return null
  const events = (q.data?.events ?? []) as {
    id: string
    action: string
    actor_name?: string
    created_at: string
  }[]
  const rows: ActivityRow[] = events.slice(0, 5).map((e) => ({
    id: e.id,
    summary: e.action.replace(/[._]/g, ' '),
    actor: e.actor_name || undefined,
    at: e.created_at,
  }))
  return <ActivitySection rows={rows} isLoading={q.isLoading} />
}

function FileActivity({ documentId }: { documentId: string }) {
  const q = useActivityForDocument(documentId)
  if (q.isError) return null
  const rows: ActivityRow[] = (q.data?.nodes ?? []).slice(0, 5).map((n) => ({
    id: n.id,
    summary: n.summary,
    actor: n.actorName || undefined,
    at: n.occurredAt,
  }))
  return <ActivitySection rows={rows} isLoading={q.isLoading} />
}

function FileDetails({ doc, workspace, onOpen }: { doc: Document; workspace?: Workspace; onOpen: () => void }) {
  const { label } = fileKind(doc.mime_type, doc.title)
  const typeLabel = doc.mime_type?.split('/').pop()?.toUpperCase() || label
  return (
    <div>
      <Preview>
        {doc.has_thumbnail && doc.thumbnail_url
          ? <img src={doc.thumbnail_url} alt="" className="max-h-[120px] rounded-lg object-contain shadow-sm" />
          : <FileTypeIcon mime={doc.mime_type} title={doc.title} size={86} />}
      </Preview>
      <Name>{doc.title}</Name>

      <Head>Info</Head>
      <div className="space-y-3.5">
        <Row k="Type" v={typeLabel} />
        <Row k="Size" v={formatFileSize(Number(doc.total_size_bytes) || 0)} />
        <Row k="Versions" v={String(doc.version_count ?? 1)} />
        <Row k="Owner" v={doc.created_by_name || '—'} />
        <Row k="Location" v={workspace?.name ?? '—'} link />
        <Row k="Modified" v={formatDateTime(doc.updated_at ?? doc.created_at)} />
        <Row k="Created" v={formatDateTime(doc.created_at)} />
      </div>

      {doc.tags?.length > 0 && (
        <>
          <Divider />
          <Head>Tags</Head>
          <div className="flex flex-wrap gap-1.5">
            {doc.tags.map((t) => (
              <span key={t} className="rounded-full border border-border bg-muted/50 px-2.5 py-0.5 text-xs font-medium text-muted-foreground">{t}</span>
            ))}
          </div>
        </>
      )}

      <FileActivity documentId={doc.id} />

      <Button className="mt-5 w-full" onClick={onOpen}>Open document</Button>
    </div>
  )
}

/* ── small primitives ── */
const Preview = ({ children }: { children: React.ReactNode }) => (
  <div className="grid place-items-center py-2">{children}</div>
)
const Name = ({ children }: { children: React.ReactNode }) => (
  <p
    title={typeof children === 'string' ? children : undefined}
    className="mb-7 mt-3.5 truncate text-center text-lg font-bold tracking-tight text-foreground"
  >
    {children}
  </p>
)
const Head = ({ children }: { children: React.ReactNode }) => (
  <div className="mb-3.5 text-[11px] font-bold uppercase tracking-[0.12em] text-muted-foreground">{children}</div>
)
const Divider = () => <div className="my-6 h-px bg-border" />
function Row({ k, v, link }: { k: string; v: string; link?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3 text-sm">
      <span className="text-muted-foreground">{k}</span>
      <span className={cn('truncate font-semibold tabular-nums', link ? 'text-primary' : 'text-foreground')}>{v}</span>
    </div>
  )
}
function Switch({ checked, onChange, disabled }: { checked: boolean; onChange: () => void; disabled?: boolean }) {
  return (
    <button
      type="button" role="switch" aria-checked={checked} disabled={disabled} onClick={onChange}
      className={cn('relative h-6 w-11 flex-none rounded-full transition-colors disabled:opacity-60', checked ? 'bg-primary' : 'bg-muted-foreground/30')}
    >
      <span className={cn('absolute top-0.5 h-5 w-5 rounded-full bg-white shadow transition-transform', checked ? 'translate-x-[22px]' : 'translate-x-0.5')} />
    </button>
  )
}
