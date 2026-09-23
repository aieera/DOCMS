/**
 * Badge variant for a document's lifecycle state.
 *
 * The document API sends the raw proto enum (`LIFECYCLE_STATE_DRAFT`),
 * while `Badge`'s variants are keyed on the short form (`draft`). Passing
 * the wire value straight through matches no variant, so the badge falls
 * back to its default grey and the status colour silently disappears —
 * which is the whole signal this screen is built around.
 */
const VARIANTS = ['draft', 'in_review', 'active', 'superseded', 'archived', 'disposed'] as const

export type LifecycleVariant = (typeof VARIANTS)[number] | 'secondary'

export function lifecycleVariant(state: string | undefined): LifecycleVariant {
  if (!state) return 'secondary'
  const key = state.toLowerCase().replace(/^lifecycle_state_/, '')
  return (VARIANTS as readonly string[]).includes(key) ? (key as LifecycleVariant) : 'secondary'
}
