import { Plus } from 'lucide-react'
import { cn } from '@/lib/cn'
import { formatFileSize } from '@/lib/formatters'
import type { Document, Folder } from '@/types/api'

/* ─────────────────────────────────────────────────────────────
 * Visual tiles for the three-column folder/file browser.
 *
 * Theme-token-driven so they flip with light/dark (the file "page"
 * fills with hsl(var(--card)); the yellow folder is intentionally the
 * one fixed brand element — iconic in both themes). Pure presentation:
 * the context menu + ⋯ button are supplied by the wrapping
 * DocumentActionsMenu (files) / FolderActionsMenu (folders).
 * ───────────────────────────────────────────────────────────── */

// Friendly yellow folder (fixed amber — the browser's signature mark).
export function FolderGlyph({ size = 56, variant = null }: { size?: number; variant?: 'music' | 'image' | 'doc' | null }) {
  let inner = ''
  if (variant === 'music') inner = `<g opacity=".92"><circle cx="27" cy="41" r="3" fill="#fff"/><circle cx="38" cy="39" r="3" fill="#fff"/><path d="M30 41V31l11-2v10" stroke="#fff" stroke-width="2.4" fill="none" stroke-linecap="round" stroke-linejoin="round"/></g>`
  if (variant === 'image') inner = `<g opacity=".92"><rect x="22" y="30" width="20" height="15" rx="2.5" fill="none" stroke="#fff" stroke-width="2.2"/><circle cx="28" cy="36" r="2" fill="#fff"/><path d="M24 44l6-6 4 4 5-5 3 3" stroke="#fff" stroke-width="2.2" fill="none" stroke-linecap="round" stroke-linejoin="round"/></g>`
  if (variant === 'doc')   inner = `<g stroke="#fff" stroke-width="2.2" stroke-linecap="round" opacity=".9"><path d="M26 34h12M26 39h12M26 44h7"/></g>`
  const svg = `<svg width="${size}" height="${Math.round(size * 0.875)}" viewBox="0 0 64 56" fill="none">
      <defs><linearGradient id="fgrad" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#fcd989"/><stop offset="1" stop-color="#f6c557"/></linearGradient></defs>
      <path d="M4 14a8 8 0 0 1 8-8h13a3 3 0 0 1 2.3 1.1l3 3.4a3 3 0 0 0 2.3 1.1h21.4a8 8 0 0 1 8 8v4H4z" fill="#f2b53f"/>
      <rect x="4" y="18" width="56" height="34" rx="9" fill="url(#fgrad)"/>${inner}
    </svg>`
  return <span className="drop-shadow-[0_8px_14px_rgba(214,168,40,0.28)]" dangerouslySetInnerHTML={{ __html: svg }} />
}

// Map a mime/extension to a short label + brand-ish color.
export function fileKind(mime: string | undefined, title: string): { label: string; color: string } {
  const m = (mime || '').toLowerCase()
  const ext = (title.split('.').pop() || '').toLowerCase()
  const is = (...xs: string[]) => xs.includes(ext)
  if (m.includes('pdf') || is('pdf')) return { label: 'PDF', color: '#EB5757' }
  if (m.includes('word') || m.includes('msword') || is('doc', 'docx')) return { label: 'DOC', color: '#2B6CB0' }
  if (m.includes('spreadsheet') || m.includes('excel') || is('xls', 'xlsx', 'csv')) return { label: 'XLS', color: '#2F9E44' }
  if (m.includes('presentation') || m.includes('powerpoint') || is('ppt', 'pptx')) return { label: 'PPT', color: '#E8590C' }
  if (m.startsWith('image/') || is('png', 'jpg', 'jpeg', 'gif', 'webp', 'svg')) return { label: 'IMG', color: '#0CA678' }
  if (m.startsWith('video/') || is('mp4', 'mov', 'webm', 'mkv')) return { label: 'VID', color: '#7048E8' }
  if (m.startsWith('audio/') || is('mp3', 'wav', 'flac')) return { label: 'AUD', color: '#1098AD' }
  if (m.includes('zip') || m.includes('compressed') || is('zip', 'rar', '7z', 'tar', 'gz')) return { label: 'ZIP', color: '#868E96' }
  if (is('fig')) return { label: 'FIG', color: '#A259FF' }
  if (is('sketch')) return { label: 'SK', color: '#FDB300' }
  if (is('psd')) return { label: 'PSD', color: '#31A8FF' }
  if (m.startsWith('text/') || is('txt', 'md')) return { label: 'TXT', color: '#868E96' }
  return { label: 'FILE', color: '#7C879B' }
}

// File "page" with a colored type badge. The page fills from the theme's
// card token (via inline hsl(var(--…))) so it flips in dark mode.
export function FileTypeIcon({ mime, title, size = 52 }: { mime?: string; title: string; size?: number }) {
  const { label, color } = fileKind(mime, title)
  return (
    <svg width={size} height={Math.round(size * 1.2)} viewBox="0 0 58 70" fill="none" className="drop-shadow-[0_8px_14px_rgba(60,66,90,0.14)]">
      <path d="M8 6a4 4 0 0 1 4-4h22l16 15v47a4 4 0 0 1-4 4H12a4 4 0 0 1-4-4z" style={{ fill: 'hsl(var(--card))', stroke: 'hsl(var(--border))' }} strokeWidth={1.4} />
      <path d="M34 2v11a4 4 0 0 0 4 4h12z" style={{ fill: 'hsl(var(--muted))' }} />
      <rect x="13" y="34" width="32" height="16" rx="4.5" fill={color} />
      <text x="29" y="45.6" textAnchor="middle" fontFamily="'Plus Jakarta Sans', system-ui, sans-serif" fontSize={label.length > 3 ? 8 : 9.5} fontWeight={800} fill="#fff">{label}</text>
    </svg>
  )
}

const tileBase =
  'group/card relative block w-full rounded-2xl border p-4 text-start transition-all duration-150 ' +
  'hover:-translate-y-0.5 hover:shadow-neu ' +
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background'
const tileState = (selected?: boolean) =>
  selected ? 'border-primary/45 bg-primary/5' : 'border-transparent hover:bg-card'

export function FolderTile({
  folder, selected, onOpen, onSelect,
}: { folder: Folder; selected?: boolean; onOpen: () => void; onSelect: () => void }) {
  // The API serialises counts as strings, so coerce — otherwise "0" + 0
  // renders as "00 items".
  const childCount = Number(folder.child_folder_count ?? folder.children_count ?? 0) || 0
  const items = (Number(folder.document_count) || 0) + childCount
  const variant = guessFolderVariant(folder.name)
  return (
    <button
      type="button"
      onClick={onSelect}
      onDoubleClick={onOpen}
      className={cn(tileBase, tileState(selected))}
      data-testid={`folder-tile-${folder.id}`}
    >
      <div className="grid h-[78px] place-items-center"><FolderGlyph size={64} variant={variant} /></div>
      <div className="mt-2 truncate text-sm font-semibold text-foreground">{folder.name}</div>
      <div className="mt-0.5 flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <span className="tabular-nums">{items} {items === 1 ? 'item' : 'items'}</span>
        {folder.visibility === 'private' && <span className="text-muted-foreground/50">· Private</span>}
      </div>
    </button>
  )
}

export function FileTile({
  doc, selected, onOpen, onSelect,
}: { doc: Document; selected?: boolean; onOpen: () => void; onSelect: () => void }) {
  return (
    <button
      type="button"
      onClick={onSelect}
      onDoubleClick={onOpen}
      className={cn(tileBase, tileState(selected))}
      data-testid={`file-tile-${doc.id}`}
    >
      <div className="grid h-[78px] place-items-center"><FileTypeIcon mime={doc.mime_type} title={doc.title} size={50} /></div>
      <div className="mt-2 truncate text-sm font-semibold text-foreground">{doc.title}</div>
      <div className="mt-0.5 text-xs font-medium text-muted-foreground tabular-nums">{formatFileSize(Number(doc.total_size_bytes) || 0)}</div>
    </button>
  )
}

/* ── list-mode rows — same props/behaviour as the tiles, compact layout ── */

// pe-10 reserves the trailing gutter where the wrapping actions-menu ⋯
// (end-1) sits, so the size/meta column never ends up underneath it.
const rowBase =
  'group/card relative flex w-full items-center gap-3 rounded-xl border ps-3 pe-10 py-2 text-start transition-colors ' +
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background'

export function FolderRow({
  folder, selected, onOpen, onSelect,
}: { folder: Folder; selected?: boolean; onOpen: () => void; onSelect: () => void }) {
  const childCount = Number(folder.child_folder_count ?? folder.children_count ?? 0) || 0
  const items = (Number(folder.document_count) || 0) + childCount
  return (
    <button
      type="button"
      onClick={onSelect}
      onDoubleClick={onOpen}
      className={cn(rowBase, tileState(selected))}
      data-testid={`folder-row-${folder.id}`}
    >
      <FolderGlyph size={30} variant={guessFolderVariant(folder.name)} />
      <span className="min-w-0 flex-1 truncate text-sm font-semibold text-foreground">{folder.name}</span>
      {folder.visibility === 'private' && <span className="shrink-0 text-xs font-medium text-muted-foreground/50">Private</span>}
      <span className="w-16 shrink-0 text-end text-xs font-medium text-muted-foreground tabular-nums">
        {items} {items === 1 ? 'item' : 'items'}
      </span>
    </button>
  )
}

export function FileRow({
  doc, selected, onOpen, onSelect,
}: { doc: Document; selected?: boolean; onOpen: () => void; onSelect: () => void }) {
  return (
    <button
      type="button"
      onClick={onSelect}
      onDoubleClick={onOpen}
      // ps-10 reserves the multi-select checkbox gutter the page
      // overlays at the row's start.
      className={cn(rowBase, 'ps-10', tileState(selected))}
      data-testid={`file-row-${doc.id}`}
    >
      <FileTypeIcon mime={doc.mime_type} title={doc.title} size={24} />
      <span className="min-w-0 flex-1 truncate text-sm font-semibold text-foreground">{doc.title}</span>
      <span className="w-20 shrink-0 text-end text-xs font-medium text-muted-foreground tabular-nums">
        {formatFileSize(Number(doc.total_size_bytes) || 0)}
      </span>
    </button>
  )
}

export function NewFolderRow({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex w-full items-center gap-3 rounded-xl border-[1.6px] border-dashed border-border px-3 py-2 text-muted-foreground transition-all hover:border-primary hover:bg-primary/5 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid="new-folder-row"
    >
      <span className="grid h-7 w-7 place-items-center rounded-lg bg-muted"><Plus className="h-4 w-4" /></span>
      <span className="text-sm font-semibold">New Folder</span>
    </button>
  )
}

export function NewFolderTile({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex min-h-[152px] flex-col items-center justify-center gap-2.5 rounded-2xl border-[1.6px] border-dashed border-border text-muted-foreground transition-all hover:border-primary hover:bg-primary/5 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid="new-folder-tile"
    >
      <span className="grid h-11 w-11 place-items-center rounded-xl bg-muted transition-colors group-hover:bg-card"><Plus className="h-5 w-5" /></span>
      <span className="text-sm font-semibold">New Folder</span>
    </button>
  )
}

// Light heuristic so a "Music"/"Pictures"/"Documents" folder gets its
// glyph like the reference, purely cosmetic.
function guessFolderVariant(name: string): 'music' | 'image' | 'doc' | null {
  const n = name.toLowerCase()
  if (/music|audio|songs/.test(n)) return 'music'
  if (/picture|image|photo|media/.test(n)) return 'image'
  if (/doc|report|brief|paper/.test(n)) return 'doc'
  return null
}
