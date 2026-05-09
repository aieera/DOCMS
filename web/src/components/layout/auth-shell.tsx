import { Database, ShieldCheck, FileLock2, Workflow } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { cn } from '@/lib/cn'

interface AuthShellProps {
  children: ReactNode
  // Compact heading text shown on the right pane above the form.
  title?: ReactNode
  description?: ReactNode
  // Optional action shown bottom-right (e.g. "Already have an account? Sign in")
  footer?: ReactNode
}

// Two-pane shell used by every unauthenticated route. Left pane
// is a brand surface with a tagline and three feature bullets —
// keeps the page from feeling empty on wide monitors. Right pane
// holds the form. Below `lg` the brand pane collapses and the
// form gets the full viewport with the brand mark above it.
export function AuthShell({ children, title, description, footer }: AuthShellProps) {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <div className="grid min-h-screen lg:grid-cols-2">
        <BrandPane />
        <div className="flex flex-col">
          <header className="flex items-center justify-between p-6 lg:hidden">
            <Link to="/login" className="flex items-center gap-2">
              <span className="flex h-7 w-7 items-center justify-center rounded-md bg-foreground text-background">
                <Database className="h-4 w-4" />
              </span>
              <span className="text-sm font-semibold tracking-tight">VaultDMS</span>
            </Link>
          </header>
          <main className="flex flex-1 items-center justify-center px-6 pb-12 pt-2 lg:px-12 lg:py-12">
            <div className="w-full max-w-sm space-y-6">
              {(title || description) && (
                <div className="space-y-1.5">
                  {title && <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>}
                  {description && <p className="text-sm text-muted-foreground">{description}</p>}
                </div>
              )}
              {children}
              {footer && <div className="pt-2 text-center text-sm text-muted-foreground">{footer}</div>}
            </div>
          </main>
        </div>
      </div>
    </div>
  )
}

const FEATURES = [
  { icon: ShieldCheck, title: 'Tenant-isolated by default', body: 'Postgres RLS plus a NOBYPASSRLS app role mean a buggy query fails closed, not open.' },
  { icon: FileLock2, title: 'Per-blob envelope encryption', body: 'Each document has its own DEK wrapped by a per-tenant KEK. Crypto-shred on delete.' },
  { icon: Workflow, title: 'Workflows, signatures, retention', body: 'Approval routing, eIDAS-grade signatures, legal-hold-aware retention out of the box.' },
]

function BrandPane() {
  return (
    <aside
      className={cn(
        'relative hidden flex-col justify-between overflow-hidden border-e border-border bg-sidebar p-12 text-sidebar-foreground lg:flex',
      )}
    >
      {/* Subtle gradient wash for depth without resorting to a real
          illustration asset. Stays cohesive with the dashboard
          chrome and respects the active theme. */}
      <div
        className="pointer-events-none absolute inset-0 opacity-60"
        aria-hidden
        style={{
          background:
            'radial-gradient(60% 50% at 20% 0%, hsl(var(--accent) / 0.6) 0%, transparent 60%), radial-gradient(40% 40% at 100% 100%, hsl(var(--primary) / 0.18) 0%, transparent 60%)',
        }}
      />
      <div className="relative">
        <Link to="/login" className="flex items-center gap-2">
          <span className="flex h-9 w-9 items-center justify-center rounded-md bg-foreground text-background">
            <Database className="h-5 w-5" />
          </span>
          <span className="text-base font-semibold tracking-tight">VaultDMS</span>
        </Link>
      </div>

      <div className="relative max-w-md space-y-6">
        <h2 className="text-3xl font-semibold leading-tight tracking-tight">
          Document management your security team will actually trust.
        </h2>
        <ul className="space-y-4">
          {FEATURES.map(({ icon: Icon, title, body }) => (
            <li key={title} className="flex gap-3">
              <span className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-sidebar-border bg-background/50">
                <Icon className="h-4 w-4 text-foreground" />
              </span>
              <div>
                <p className="text-sm font-medium leading-tight">{title}</p>
                <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">{body}</p>
              </div>
            </li>
          ))}
        </ul>
      </div>

      <p className="relative text-xs text-muted-foreground">
        © {new Date().getFullYear()} VaultDMS · {' '}
        <a href="#" className="hover:text-foreground">Status</a> · {' '}
        <a href="#" className="hover:text-foreground">Privacy</a> · {' '}
        <a href="#" className="hover:text-foreground">Docs</a>
      </p>
    </aside>
  )
}
