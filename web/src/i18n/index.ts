// ADR 0106 — i18n foundation (react-i18next + HTTP backend).
//
// What this module is responsible for:
//   * Initialising the i18next singleton exactly once at app bootstrap.
//   * Telling the browser which BCP-47 tag + writing direction (rtl|ltr)
//     to apply to <html> before the first paint.
//   * Exposing a small surface (`SUPPORTED_LOCALES`, `applyDocumentDir`)
//     other modules use so we don't sprinkle locale knowledge across
//     LanguageSelector, authStore, and main.tsx.
//
// What it explicitly does NOT do:
//   * Talk to the backend. The /auth/me hydrate path calls
//     i18n.changeLanguage(user.locale) directly — that lives in the
//     auth store. Keeping the persistence wire out of this module
//     means tests can spin up i18n without mocking the API client.
//   * Render any Suspense fallback. We set `useSuspense: false` so
//     useTranslation() returns ready=false during namespace loads;
//     the outer Suspense in main.tsx is for route-level code splitting,
//     not for i18n. The two are deliberately decoupled.

import i18n from 'i18next'
import HttpBackend from 'i18next-http-backend'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

export type SupportedLocale = 'en' | 'ar'

export const SUPPORTED_LOCALES: SupportedLocale[] = ['en', 'ar']

// Namespaces match the eight modules the audit identified. Adding a
// new namespace means a new bundle in web/public/locales/{lng}/{ns}.json
// AND adding it to this array — otherwise i18next won't lazy-load it.
export const NAMESPACES = [
  'common',
  'auth',
  'documents',
  'admin',
  'signatures',
  'intelligence',
  'workflows',
  'errors',
] as const

export const DEFAULT_NAMESPACE = 'common'

// applyDocumentDir flips the <html> dir / lang attributes. Called on
// init AND every time the user picks a new locale. We do it from a
// helper rather than inline so the Vitest harness (which has no
// real <html>) can stub it.
export function applyDocumentDir(lng: string) {
  if (typeof document === 'undefined') return
  const isRTL = lng.startsWith('ar')
  document.documentElement.lang = lng
  document.documentElement.dir = isRTL ? 'rtl' : 'ltr'
}

// initI18n returns the resolved i18n instance so callers can await
// it before rendering. The promise resolves even if a namespace 404s
// — i18next falls back to the key string and logs a warning.
export function initI18n() {
  return i18n
    .use(HttpBackend)
    .use(LanguageDetector)
    .use(initReactI18next)
    .init({
      fallbackLng:   'en',
      supportedLngs: SUPPORTED_LOCALES,
      ns:            NAMESPACES as unknown as string[],
      defaultNS:     DEFAULT_NAMESPACE,
      // English is the canonical key set. Missing translations
      // surface as the English copy rather than the raw key — the
      // /admin coverage page will name-and-shame missing keys.
      load:          'languageOnly',
      interpolation: { escapeValue: false }, // React already escapes.
      backend: {
        // Served from web/public/locales/ at runtime. In dev Vite
        // serves these directly; in prod they're static assets.
        loadPath: '/locales/{{lng}}/{{ns}}.json',
      },
      detection: {
        // Order: server-persisted choice (set by authStore after
        // /auth/me) > cookie > localStorage > navigator. The
        // authStore writes both cookie + localStorage so the
        // pre-login bootstrap respects the most recent user's
        // choice on this browser.
        order:           ['cookie', 'localStorage', 'navigator'],
        caches:          ['cookie', 'localStorage'],
        cookieMinutes:   60 * 24 * 365, // 1y
        lookupCookie:    'dms_locale',
        lookupLocalStorage: 'dms_locale',
      },
      react: {
        // ADR 0106: the outer Suspense in main.tsx is route-level
        // (TanStack Router). We don't want useTranslation() to
        // suspend whenever a new namespace lazy-loads — that would
        // unmount the entire route tree. Returning ready=false from
        // the hook lets components render an English-ish skeleton
        // and re-render when the bundle arrives.
        useSuspense: false,
      },
    })
    .then((t) => {
      // Initial dir/lang stamp. changeLanguage() does this too via
      // the 'languageChanged' event listener below, but the initial
      // resolution doesn't fire that event.
      applyDocumentDir(i18n.language || 'en')
      i18n.on('languageChanged', applyDocumentDir)
      return t
    })
}

export default i18n
