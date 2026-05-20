// ADR 0106 — confirm the i18n bootstrap shape.
//
// We deliberately don't assert real string lookups here — translations
// land in Prompt 4. What we DO assert: init resolves; the default
// language is the configured fallback; namespaces are registered; the
// resolver returns the key when a translation is missing (the keys-as-
// strings fallback the whole architecture relies on).
import { describe, it, expect, beforeAll, vi } from 'vitest'

import i18n, { NAMESPACES, SUPPORTED_LOCALES, initI18n, applyDocumentDir } from '@/i18n'

// Stub fetch so the HTTP backend doesn't 404 against jsdom — every
// namespace resolves to {} which is enough for init to consider the
// bundle "loaded".
beforeAll(async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })))
  await initI18n()
})

describe('i18n init', () => {
  it('resolves with English as the fallback', () => {
    expect(i18n.options.fallbackLng).toEqual(['en'])
  })

  it('registers every namespace from NAMESPACES', () => {
    const opt = i18n.options.ns
    const opts = Array.isArray(opt) ? opt : opt ? [opt] : []
    for (const ns of NAMESPACES) {
      expect(opts).toContain(ns)
    }
  })

  it('exposes the supported locales it was configured with', () => {
    // i18next normalises `supportedLngs` and appends 'cimode' for its
    // own debugging flag — filter that out before comparing.
    const got = (i18n.options.supportedLngs || []).filter((l) => l !== 'cimode')
    for (const expected of SUPPORTED_LOCALES) {
      expect(got).toContain(expected)
    }
  })

  it('falls back to the key string when a translation is missing', () => {
    // Empty bundles + the keys-as-strings fallback means t('foo.bar')
    // returns 'foo.bar' rather than throwing.
    expect(i18n.t('definitely.not.translated.yet')).toBe('definitely.not.translated.yet')
  })

  it('applyDocumentDir flips <html> direction for Arabic', () => {
    applyDocumentDir('ar')
    expect(document.documentElement.dir).toBe('rtl')
    expect(document.documentElement.lang).toBe('ar')
    applyDocumentDir('en')
    expect(document.documentElement.dir).toBe('ltr')
    expect(document.documentElement.lang).toBe('en')
  })

  it('useSuspense is disabled (outer route Suspense owns boundaries)', () => {
    // The init module sets react.useSuspense = false. We assert via
    // the i18next options because react-i18next reads this lazily.
    // Casting through unknown because the i18next types don't expose
    // the react sub-object on options.
    const react = (i18n.options as unknown as { react?: { useSuspense?: boolean } }).react
    expect(react?.useSuspense).toBe(false)
  })
})
