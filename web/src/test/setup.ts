import '@testing-library/jest-dom/vitest'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { cleanup } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import { server } from './mocks/server'
import enCommon from '../../public/locales/en/common.json'

// i18n for the test run. Without this, useTranslation()'s `t` echoes the
// key, so a component that renders t('dashboard.widgets.lifecycle_title')
// puts the literal "dashboard.widgets.lifecycle_title" on screen and
// every assertion written against the real copy fails.
//
// The English bundle is imported rather than stubbed on purpose: these
// tests assert user-visible text, so they should read the same strings
// the app ships. It also means a key deleted from the catalogue without
// updating its call site fails a test instead of silently rendering a
// dotted key path to a user.
//
// Synchronous init (resources inline, no HTTP backend) so no test has to
// await a namespace load.
void i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  ns: ['common'],
  defaultNS: 'common',
  resources: { en: { common: enCommon } },
  interpolation: { escapeValue: false },
  react: { useSuspense: false },
})

// MSW lifecycle: start a Node-based server for the duration of the test
// run, reset handlers between tests so one test can't bleed assertions
// into another, and stop it cleanly at the end.
beforeAll(() => server.listen({ onUnhandledRequest: 'bypass' }))
afterEach(() => {
  cleanup()
  server.resetHandlers()
})
afterAll(() => server.close())

// jsdom doesn't implement ResizeObserver; many Radix/Tanstack components
// use it internally. Stub with a no-op so components don't crash on mount.
class ResizeObserverMock {
  observe() {}
  unobserve() {}
  disconnect() {}
}
;(globalThis as unknown as { ResizeObserver: typeof ResizeObserverMock }).ResizeObserver = ResizeObserverMock

// jsdom implements neither the Pointer Capture API nor scrollIntoView, both of
// which Radix Select calls when its listbox opens. Without these stubs any test
// that clicks a Select trigger throws instead of opening the menu.
if (typeof Element !== 'undefined') {
  Element.prototype.hasPointerCapture ??= () => false
  Element.prototype.setPointerCapture ??= () => {}
  Element.prototype.releasePointerCapture ??= () => {}
  Element.prototype.scrollIntoView ??= () => {}
}

// jsdom lacks window.matchMedia — sonner and some Radix primitives
// call it to query prefers-reduced-motion.
if (typeof window !== 'undefined' && !window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
}
