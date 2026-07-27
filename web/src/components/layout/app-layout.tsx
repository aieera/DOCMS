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
    // h-dvh (not min-h-screen): the shell owns exactly one viewport and
    // `main` below is the single scroll container. Pages that manage
    // their own internal scrolling (e.g. the workspace file browser)
    // get a bounded height at every zoom level instead of growing the
    // body and sprouting a second scrollbar.
    <div className="relative isolate h-dvh bg-background text-foreground">
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
          // Navy backdrop so the content panel's rounded top-left corner
          // reveals navy where it meets the navy sidebar + topbar.
          'flex h-full flex-col bg-sidebar transition-[padding] duration-200 ease-out motion-reduce:transition-none',
          // The sidebar is hidden below lg; on lg+ we reserve space
          // for it so the content doesn't slide under the panel.
          'lg:ps-[260px]',
          collapsed && 'lg:ps-16',
        )}
      >
        <AppTopbar onOpenMobileNav={() => setMobileOpen(true)} />
        {/* Content panel — curved top-left corner tucks the light canvas
            under the navy topbar/sidebar like a nested card. */}
        <main id="main-content" className="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto rounded-tl-[28px] bg-background">
          <div className="mx-auto flex w-full flex-1 flex-col p-4 sm:p-6 lg:p-8">{children}</div>
        </main>
      </div>
    </div>
  )
}
