import { useState, type ReactNode } from 'react'
import { cn } from '@/lib/cn'
import { useUIStore } from '@/store/uiStore'
import { AppSidebar, MobileSidebar } from './app-sidebar'
import { AppTopbar } from './app-topbar'

// New canonical app shell. Composes the sidebar (desktop persistent
// + mobile drawer) and the sticky topbar around route content. The
// previous AppLayout.tsx is kept on disk so any direct imports still
// work, but all _authenticated routes flow through this one.
export function AppLayout({ children }: { children: ReactNode }) {
  const collapsed = useUIStore((s) => s.sidebarCollapsed)
  const [mobileOpen, setMobileOpen] = useState(false)
  return (
    <div className="relative isolate min-h-screen bg-background text-foreground">
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:absolute focus:top-2 focus:start-2 focus:z-50 focus:rounded-md focus:bg-foreground focus:px-3 focus:py-1.5 focus:text-sm focus:text-background"
      >
        Skip to content
      </a>
      <AppSidebar />
      <MobileSidebar open={mobileOpen} onOpenChange={setMobileOpen} />
      <div
        className={cn(
          'flex min-h-screen flex-col transition-[padding] duration-200 ease-out motion-reduce:transition-none',
          // The sidebar is hidden below lg; on lg+ we reserve space
          // for it so the content doesn't slide under the panel.
          'lg:ps-[260px]',
          collapsed && 'lg:ps-16',
        )}
      >
        <AppTopbar onOpenMobileNav={() => setMobileOpen(true)} />
        <main id="main-content" className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div className="mx-auto flex w-full flex-1 flex-col p-4 sm:p-6 lg:p-8">{children}</div>
        </main>
      </div>
    </div>
  )
}
