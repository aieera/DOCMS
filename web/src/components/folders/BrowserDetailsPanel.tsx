import { toast } from 'sonner'
import { FolderOpen } from 'lucide-react'

import { cn } from '@/lib/cn'
import { formatFileSize, formatDateTime } from '@/lib/formatters'
import { Button } from '@/components/ui/shadcn/button'
import { useSetFolderVisibility } from '@/hooks/useFolders'
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
}

export function BrowserDetailsPanel({ selection, workspace, onOpenFolder, onOpenFile }: Props) {
  return (
    <aside className="hidden w-[300px] flex-none border-s border-border xl:block">
      <div className="sticky top-0 max-h-[calc(100vh-7rem)] overflow-y-auto p-6">
        {selection === null ? (
          <EmptyState workspace={workspace} />
        ) : selection.type === 'folder' ? (
          <FolderDetails folder={selection.folder} workspace={workspace} onOpen={() => onOpenFolder(selection.folder)} />
        ) : (
          <FileDetails doc={selection.doc} workspace={workspace} onOpen={() => onOpenFile(selection.doc)} />
        )}
      </div>
    </aside>
  )
}

function EmptyState({ workspace }: { workspace?: Workspace }) {
  return (
    <div className="flex flex-col items-center pt-6 text-center">
      <span className="grid h-16 w-16 place-items-center rounded-2xl bg-muted text-muted-foreground">
        <FolderOpen className="h-7 w-7" />
      </span>
      <p className="mt-4 text-base font-semibold text-foreground">{workspace?.name ?? 'Workspace'}</p>
      <p className="mt-1 text-sm text-muted-foreground">Select a folder or file to see its details.</p>
      {workspace && (
        <div className="mt-6 w-full space-y-3 text-start">
          <Head>Info</Head>
          <Row k="Type" v="Workspace" />
          <Row k="Documents" v={workspace.document_count.toLocaleString()} />
          <Row k="Members" v={(workspace.member_count ?? 0).toLocaleString()} />
          <Row k="Created" v={formatDateTime(workspace.created_at)} />
        </div>
      )}
    </div>
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

      <Button variant="outline" className="mt-5 w-full gap-2" onClick={onOpen}>
        <FolderOpen className="h-4 w-4" /> Open folder
      </Button>
    </div>
  )
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

      <Button className="mt-5 w-full" onClick={onOpen}>Open document</Button>
    </div>
  )
}

/* ── small primitives ── */
const Preview = ({ children }: { children: React.ReactNode }) => (
  <div className="grid place-items-center py-2">{children}</div>
)
const Name = ({ children }: { children: React.ReactNode }) => (
  <p className="mb-7 mt-3.5 truncate text-center text-lg font-bold tracking-tight text-foreground">{children}</p>
)
const Head = ({ children }: { children: React.ReactNode }) => (
  <div className="mb-3.5 text-[11px] font-bold uppercase tracking-[0.12em] text-muted-foreground/70">{children}</div>
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
