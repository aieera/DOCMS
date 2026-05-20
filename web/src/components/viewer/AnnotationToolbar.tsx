// ADR 0067 — annotation toolbar. One component renders the right
// tool palette for PDF / image / video; the parent viewer chooses
// which mode to expose by passing `kind`.
//
// Toolbar emits the selected `mode` via onModeChange. The actual
// canvas interaction lives in the per-kind overlay component
// (PDFAnnotationLayer / ImageAnnotationLayer / VideoAnnotationPins);
// the toolbar only owns mode selection + the visibility toggle.

import type { ReactNode } from 'react'
import { Highlighter, Underline, Strikethrough, MessageSquarePlus, Pencil, Square, Circle as CircleIcon, ArrowRight, Eye, EyeOff, type LucideIcon } from 'lucide-react'
export type AnnotationKind = 'pdf' | 'image' | 'video'

export type PDFMode      = 'highlight' | 'underline' | 'strikethrough' | 'note' | 'drawing'
export type ImageMode    = 'rect' | 'ellipse' | 'arrow' | 'note'
export type VideoMode    = 'pin'
export type ToolMode     = PDFMode | ImageMode | VideoMode | null

interface Props {
  kind: AnnotationKind
  mode: ToolMode
  onModeChange: (m: ToolMode) => void
  visible: boolean
  onToggleVisible: (v: boolean) => void
  // Disabled when the user lacks both `edit` AND
  // `annotation.create` on the document. The viewer hides write
  // tools but keeps the visibility toggle so a read-only viewer
  // can still hide the overlay.
  canCreate: boolean
}

const PDF_TOOLS: Array<{ mode: PDFMode; label: string; icon: LucideIcon }> = [
  { mode: 'highlight',     label: 'Highlight',     icon: Highlighter },
  { mode: 'underline',     label: 'Underline',     icon: Underline },
  { mode: 'strikethrough', label: 'Strike-through', icon: Strikethrough },
  { mode: 'note',          label: 'Comment',       icon: MessageSquarePlus },
  { mode: 'drawing',       label: 'Draw',          icon: Pencil },
]

const IMAGE_TOOLS: Array<{ mode: ImageMode; label: string; icon: LucideIcon }> = [
  { mode: 'rect',    label: 'Rectangle', icon: Square },
  { mode: 'ellipse', label: 'Ellipse',   icon: CircleIcon },
  { mode: 'arrow',   label: 'Arrow',     icon: ArrowRight },
  { mode: 'note',    label: 'Comment',   icon: MessageSquarePlus },
]

const VIDEO_TOOLS: Array<{ mode: VideoMode; label: string; icon: LucideIcon }> = [
  { mode: 'pin', label: 'Pin comment at current time', icon: MessageSquarePlus },
]

export function AnnotationToolbar({ kind, mode, onModeChange, visible, onToggleVisible, canCreate }: Props) {
  const tools = kind === 'pdf' ? PDF_TOOLS : kind === 'image' ? IMAGE_TOOLS : VIDEO_TOOLS

  return (
    <div className="flex items-center gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-1" data-testid="annotation-toolbar">
      {canCreate && tools.map((t) => (
        <ToolButton
          key={t.mode}
          active={mode === t.mode}
          onClick={() => onModeChange(mode === t.mode ? null : t.mode)}
          title={t.label}
          testid={`tool-${kind}-${t.mode}`}
        >
          <t.icon className="h-4 w-4" />
        </ToolButton>
      ))}
      {/* Visibility toggle is always available — even read-only
          viewers can hide the overlay. */}
      <span className="mx-1 h-5 w-px bg-[var(--color-border)]" />
      <ToolButton
        active={visible}
        onClick={() => onToggleVisible(!visible)}
        title={visible ? 'Hide annotations' : 'Show annotations'}
        testid="annotation-visibility-toggle"
      >
        {visible ? <Eye className="h-4 w-4" /> : <EyeOff className="h-4 w-4" />}
      </ToolButton>
    </div>
  )
}

function ToolButton({ active, onClick, title, testid, children }: {
  active: boolean
  onClick: () => void
  title: string
  testid: string
  children: ReactNode
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      aria-label={title}
      data-testid={testid}
      className={`rounded p-1.5 text-[var(--color-text-primary)] transition ${
        active ? 'bg-[var(--color-primary)]/15 text-[var(--color-primary)]' : 'hover:bg-slate-100 dark:hover:bg-slate-800'
      }`}
    >
      {children}
    </button>
  )
}
