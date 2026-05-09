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
    <div className="min-h-screen bg-background text-foreground">
      <AppSidebar />
      <MobileSidebar open={mobileOpen} onOpenChange={setMobileOpen} />
      <div
        className={cn(
          'flex min-h-screen flex-col transition-[padding] duration-200 ease-out',
          // The sidebar is hidden below lg; on lg+ we reserve space
          // for it so the content doesn't slide under the panel.
          'lg:ps-[260px]',
          collapsed && 'lg:ps-16',
        )}
      >
        <AppTopbar onOpenMobileNav={() => setMobileOpen(true)} />
        <main className="flex-1">
          <div className="mx-auto w-full max-w-screen-2xl p-4 sm:p-6 lg:p-8">{children}</div>
        </main>
      </div>
    </div>
  )
}
