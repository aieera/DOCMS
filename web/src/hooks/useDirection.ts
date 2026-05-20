// ADR 0108 — useDirection.
//
// Returns 'rtl' | 'ltr' derived from the active i18n language. The
// hook subscribes to react-i18next so consumers re-render the
// instant the user flips locales — no manual MutationObserver on
// <html dir> needed.
//
// Why not just read document.documentElement.dir?
//   * It's correct at render time but doesn't trigger re-renders.
//     Components that conditionally render based on direction
//     (DirectionalIcon, drawer side props, animation directions)
//     need the reactive form.
//   * `i18n.resolvedLanguage` is the single source of truth — it's
//     what applyDocumentDir() reads from when stamping the
//     attribute. Reading the same source avoids a one-frame
//     mismatch during locale switches.
import { useTranslation } from 'react-i18next'

export type Direction = 'ltr' | 'rtl'

// RTL locales we accept today. Extending this list = a 1-line
// change; the i18n config in src/i18n/index.ts is the authoritative
// list of supported languages.
const RTL_LANGS = new Set<string>(['ar'])

export function useDirection(): Direction {
  const { i18n } = useTranslation()
  const lng = (i18n.resolvedLanguage ?? i18n.language ?? 'en').split('-')[0]
  return RTL_LANGS.has(lng) ? 'rtl' : 'ltr'
}
