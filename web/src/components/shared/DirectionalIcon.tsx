// ADR 0108 — DirectionalIcon.
//
// Lucide icons are static SVGs — ChevronRight always points right
// regardless of <html dir>. For NAVIGATIONAL icons (back / next /
// breadcrumb separator / collapse arrow) we want them to mirror
// when the document is RTL, so a "next" arrow visually points the
// way the user reads.
//
// For PHYSICAL icons (a rewind button, a music-player back-10s
// indicator) the arrow must NEVER mirror because rewind is always
// "backwards in time", not "backwards in reading order". Those
// call sites should keep using the raw lucide import OR pass
// `flip={false}` to DirectionalIcon — the comment on the line
// above keeps reviewers honest:
//
//   // physical-direction: intentional — rewind icon
//   <DirectionalIcon name="ArrowLeft" flip={false} />
//
// We support only horizontal icons here. Vertical (ChevronUp,
// ChevronDown, ArrowUp, ArrowDown) need no flip — direction-
// independent.
import {
  ChevronLeft, ChevronRight,
  ArrowLeft, ArrowRight,
  ArrowLeftFromLine, ArrowRightFromLine,
  ArrowLeftToLine,   ArrowRightToLine,
  ChevronsLeft, ChevronsRight,
  CornerDownLeft, CornerDownRight,
  CornerUpLeft,   CornerUpRight,
  MoveLeft, MoveRight,
  type LucideIcon, type LucideProps,
} from 'lucide-react'
import { useDirection } from '@/hooks/useDirection'
import { cn } from '@/lib/cn'

// Curated allow-list of horizontal directional icons. Keep this
// list in sync with the migration script
// (web/scripts/migrate-directional-icons.mjs).
const ICONS = {
  ChevronLeft, ChevronRight,
  ChevronsLeft, ChevronsRight,
  ArrowLeft, ArrowRight,
  ArrowLeftFromLine, ArrowRightFromLine,
  ArrowLeftToLine,   ArrowRightToLine,
  CornerDownLeft, CornerDownRight,
  CornerUpLeft,   CornerUpRight,
  MoveLeft, MoveRight,
} as const satisfies Record<string, LucideIcon>

export type DirectionalIconName = keyof typeof ICONS

export interface DirectionalIconProps extends LucideProps {
  name: DirectionalIconName
  /**
   * Default: true — flip horizontally under dir="rtl" so the arrow
   * follows reading order. Pass `false` for genuinely physical
   * icons (rewind, video back-10s) that must NEVER mirror.
   */
  flip?: boolean
}

export function DirectionalIcon({
  name,
  flip = true,
  className,
  ...rest
}: DirectionalIconProps) {
  const dir = useDirection()
  const Icon = ICONS[name]
  // We could use Tailwind's `rtl:-scale-x-100` modifier (provided
  // by tailwindcss-rtl via the `rtl:` variant), but the inline
  // class + dir check is clearer at call sites and survives any
  // CSS rebuild order issues. Both paths produce the same DOM.
  const shouldFlip = flip && dir === 'rtl'
  return (
    <Icon
      className={cn(shouldFlip && '-scale-x-100', className)}
      aria-hidden
      {...rest}
    />
  )
}
