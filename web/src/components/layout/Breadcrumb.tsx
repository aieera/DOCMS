import { Link } from '@tanstack/react-router'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

interface Crumb { label: string; to?: string; params?: Record<string, string> }

export function Breadcrumb({ items }: { items: Crumb[] }) {
  return (
    <nav aria-label="Breadcrumb" className="mb-4 flex items-center gap-1 text-sm">
      {items.map((item, i) => (
        <span key={i} className="flex items-center gap-1">
          {i > 0 && <DirectionalIcon name="ChevronRight" className="h-3.5 w-3.5 text-[var(--color-text-secondary)]" />}
          {item.to ? (
            <Link to={item.to} params={item.params} className="text-[var(--color-text-secondary)] hover:text-[var(--color-text)]">{item.label}</Link>
          ) : (
            <span className="font-medium">{item.label}</span>
          )}
        </span>
      ))}
    </nav>
  )
}
