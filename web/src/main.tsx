import React, { Suspense } from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider, createRouter } from '@tanstack/react-router'
import { Toaster as SonnerToaster } from 'sonner'
import { ThemeProvider, useTheme } from '@/components/layout/theme-provider'
import { routeTree } from './routeTree.gen'
// ADR 0106 — must run BEFORE first React render so the very first
// paint already has dir/lang stamped on <html>. Importing for side
// effects + awaiting initI18n() inside the bootstrap below.
import { initI18n } from '@/i18n'
import './styles/globals.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 60_000, retry: 1 },
  },
})

const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register { router: typeof router }
}

// Sonner needs to know the resolved theme so its toast surfaces match
// the rest of the app instead of always rendering the system colour.
function ThemedSonner() {
  const { resolved } = useTheme()
  return (
    <SonnerToaster
      theme={resolved}
      richColors
      closeButton
      position="bottom-right"
      toastOptions={{ classNames: { toast: 'border border-border' } }}
    />
  )
}

// Minimal pre-router fallback. Renders during the brief window
// between `createRoot` and i18n's first namespace bundle resolving
// — and any time a lazy route bundle is in flight. Lives inline
// (not a real component import) so it's still SSR-safe and never
// holds a translated string itself.
function LoadingScreen() {
  return (
    <div className="flex h-screen items-center justify-center bg-background text-sm text-muted-foreground">
      Loading…
    </div>
  )
}

// Kick off i18n first so the first paint already has dir/lang.
// initI18n's promise resolves on first-namespace load; we await it
// then mount. Errors don't block — a CDN miss on /locales/en/common.json
// should still let the app boot in keys-as-fallback mode.
initI18n()
  .catch((err) => {
    console.warn('i18n init failed; falling back to keys-as-strings', err)
  })
  .finally(() => {
    ReactDOM.createRoot(document.getElementById('root')!).render(
      <React.StrictMode>
        <ThemeProvider>
          <QueryClientProvider client={queryClient}>
            {/* Route-level Suspense. Distinct from i18n's
                useSuspense=false; this catches TanStack Router lazy
                bundles + any future React.lazy() splits. */}
            <Suspense fallback={<LoadingScreen />}>
              <RouterProvider router={router} />
            </Suspense>
            <ThemedSonner />
          </QueryClientProvider>
        </ThemeProvider>
      </React.StrictMode>,
    )
  })
