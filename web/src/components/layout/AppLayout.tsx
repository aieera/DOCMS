import { type ReactNode } from 'react'
import { useUIStore } from '@/store/uiStore'
import { cn } from '@/lib/cn'
import { Sidebar } from './Sidebar'
import { Header } from './Header'
import { SecurityBanner } from '@/features/admin/security/SecurityBanner'

export function AppLayout({ children }: { children: ReactNode }) {
  const collapsed = useUIStore((s) => s.sidebarCollapsed)
  return (
    <div className="flex h-screen overflow-hidden">
      <Sidebar />
      <div className={cn('flex flex-1 flex-col transition-all duration-200', collapsed ? 'ms-16' : 'ms-[280px]')}>
        <Header />
        {/* ADR 0033 — hidden for non-admins + when all gates pass. */}
        <SecurityBanner />
        <main className="flex-1 overflow-y-auto p-6">{children}</main>
      </div>
    </div>
  )
}
