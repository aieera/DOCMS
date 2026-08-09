import { useEffect } from 'react'
import { createRootRoute, Link, Outlet, useRouterState } from '@tanstack/react-router'
import { ErrorBoundary } from '@/components/shared/ErrorBoundary'
import { documentTitleFor, formatDocumentTitle } from '@/lib/documentTitle'

// DocumentTitle keeps <title> in step with the active route so tabs,
// history entries and screen-reader page announcements name the page.
// See lib/documentTitle.ts for the resolution order and for why this
// assigns document.title instead of rendering TanStack's <HeadContent />.
function DocumentTitle() {
  const title = useRouterState({
    select: (s) => documentTitleFor(s.matches, s.location.pathname),
  })
  useEffect(() => {
    document.title = title
  }, [title])
  return null
}

// ThemeProvider in main.tsx is the single source of truth for the
// dark class. The previous useThemeInit() hook (mirrored uiStore.theme
// onto html.dark) raced with it and stomped the value back to light
// on every route mount.
function RootLayout() {
  return (
    <ErrorBoundary>
      <DocumentTitle />
      <Outlet />
    </ErrorBoundary>
  )
}

// Catch-all 404 — without this, TanStack Router falls through to the
// dev-server's raw plaintext "Not Found" on a black background. We
// render a themed page with a route back home instead.
function NotFoundPage() {
  // The 404 renders outside any matched route, so DocumentTitle's
  // pathname fallback would name a page that doesn't exist.
  useEffect(() => {
    document.title = formatDocumentTitle('Page not found')
  }, [])
  return (
    <div className="flex min-h-screen flex-col items-center justify-center gap-4 bg-background px-6 text-center">
      <p className="text-sm font-mono uppercase tracking-wider text-muted-foreground">404</p>
      <h1 className="text-2xl font-semibold tracking-tight">Page not found</h1>
      <p className="max-w-md text-sm text-muted-foreground">
        The URL you opened doesn&apos;t match a route in SeDoc. It may have been moved
        or the link could be stale.
      </p>
      <Link
        to="/"
        className="mt-2 inline-flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
      >
        Go to home
      </Link>
    </div>
  )
}

export const Route = createRootRoute({
  component: RootLayout,
  notFoundComponent: NotFoundPage,
})
