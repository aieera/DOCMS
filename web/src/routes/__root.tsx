import { createRootRoute, Outlet } from '@tanstack/react-router'
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

export const Route = createRootRoute({ component: RootLayout })
