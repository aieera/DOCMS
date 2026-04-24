import { createFileRoute } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'
import { AckBanner } from '@/components/shared/AckBanner'
import { useAuthStore } from '@/store/authStore'
import { FileText, FolderOpen, Search, Upload } from 'lucide-react'
import { Link } from '@tanstack/react-router'

function DashboardPage() {
  const user = useAuthStore((s) => s.user)

  return (
    <div>
      <PageHeader
        title={`Welcome back, ${user?.display_name?.split(' ')[0] || 'there'}`}
        description="Here's what's happening in your workspace"
      />
      <AckBanner />
      <div className="grid grid-cols-4 gap-4">
        {[
          { icon: FileText, label: 'Recent Documents', value: '—', to: '/workspaces' },
          { icon: FolderOpen, label: 'Workspaces', value: '—', to: '/workspaces' },
          { icon: Search, label: 'Search', value: '⌘K', to: '/search' },
          { icon: Upload, label: 'Upload', value: 'Drag & drop', to: '/workspaces' },
        ].map(({ icon: Icon, label, value, to }) => (
          <Link key={label} to={to} className="flex items-center gap-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 transition-shadow hover:shadow-md">
            <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-[var(--color-accent)]">
              <Icon className="h-5 w-5 text-[var(--color-primary)]" />
            </div>
            <div>
              <p className="text-sm font-medium">{label}</p>
              <p className="text-xs text-[var(--color-text-secondary)]">{value}</p>
            </div>
          </Link>
        ))}
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/')({ component: DashboardPage })
