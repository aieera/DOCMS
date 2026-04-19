import { createRootRoute, Outlet } from '@tanstack/react-router'
import { ErrorBoundary } from '@/components/shared/ErrorBoundary'
import { useThemeInit } from '@/hooks/useTheme'

function RootLayout() {
  useThemeInit()
  return (
    <ErrorBoundary>
      <Outlet />
    </ErrorBoundary>
  )
}

export const Route = createRootRoute({ component: RootLayout })
