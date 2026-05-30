import { useTranslation } from 'react-i18next'
import { CheckCircle2, AlertCircle, Loader2, XCircle, Clock } from 'lucide-react'
import { cn } from '@/lib/cn'
import type { InstanceStatus } from '@/api/workflows'

interface Props {
  status: InstanceStatus | undefined
  size?: 'sm' | 'md'
  className?: string
}

// WorkflowStatusBadge maps the WorkflowInstance.status enum to a
// coloured pill with an icon. Renders nothing when status is
// undefined (no active workflow) so the same component can be
// dropped into card grids without a wrapper conditional.
//
// Color tokens align with the rest of the app: success for completed,
// destructive for failed, warning for cancelled, primary (mustard)
// for running, muted for pending. Dark mode handled via the same
// /15 alpha pattern used elsewhere in the app.
export function WorkflowStatusBadge({ status, size = 'md', className }: Props) {
  const { t } = useTranslation('workflows')
  if (!status) return null
  const spec = SPEC[status]
  const Icon = spec.icon
  const iconSize = size === 'sm' ? 'h-3 w-3' : 'h-3.5 w-3.5'
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium',
        size === 'sm' && 'text-[10px]',
        spec.cls,
        className,
      )}
      data-testid="workflow-status-badge"
    >
      <Icon className={cn(iconSize, spec.iconAnim)} />
      {t(`status.${status}`)}
    </span>
  )
}

const SPEC: Record<InstanceStatus, { icon: typeof Clock; cls: string; iconAnim?: string }> = {
  pending: {
    icon: Clock,
    cls: 'bg-muted text-muted-foreground',
  },
  running: {
    icon: Loader2,
    cls: 'bg-primary/15 text-primary',
    iconAnim: 'animate-spin',
  },
  completed: {
    icon: CheckCircle2,
    cls: 'bg-success/15 text-success',
  },
  failed: {
    icon: XCircle,
    cls: 'bg-destructive/15 text-destructive',
  },
  cancelled: {
    icon: AlertCircle,
    cls: 'bg-warning/15 text-warning',
  },
}
