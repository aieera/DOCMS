import { createRootRoute, Link, Outlet } from '@tanstack/react-router'
import { ErrorBoundary } from '@/components/shared/ErrorBoundary'

// ThemeProvider in main.tsx is the single source of truth for the
// dark class. The previous useThemeInit() hook (mirrored uiStore.theme
// onto html.dark) raced with it and stomped the value back to light
// on every route mount.
function RootLayout() {
  return (
    <ErrorBoundary>
      <Outlet />
    </ErrorBoundary>
  )
}

// Catch-all 404 — without this, TanStack Router falls through to the
// dev-server's raw plaintext "Not Found" on a black background. We
// render a themed page with a route back home instead.
function NotFoundPage() {
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
