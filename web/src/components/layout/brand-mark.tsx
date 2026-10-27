import { Database } from 'lucide-react'

/**
 * The SeDoc brand tile: a 32px primary square holding the Database glyph.
 * Shared by the sidebar's brand row and the sign-in pages so the two can
 * never drift apart. Decorative — the "SeDoc" wordmark (or an aria-label
 * on the surrounding link) names it.
 */
export function BrandMark() {
  return (
    <span
      aria-hidden
      className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground"
    >
      <Database className="h-4 w-4" />
    </span>
  )
}
