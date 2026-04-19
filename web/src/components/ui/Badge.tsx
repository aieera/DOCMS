import { cn } from '@/lib/cn'

const colorMap: Record<string, string> = {
  draft: 'bg-slate-100 text-slate-700 dark:bg-slate-700 dark:text-slate-300',
  in_review: 'bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-200',
  active: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200',
  superseded: 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200',
  archived: 'bg-slate-200 text-slate-600 dark:bg-slate-600 dark:text-slate-300',
  disposed: 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200',
  default: 'bg-slate-100 text-slate-700 dark:bg-slate-700 dark:text-slate-300',
}

interface BadgeProps {
  variant?: string
  children: React.ReactNode
  className?: string
}

export function Badge({ variant = 'default', children, className }: BadgeProps) {
  return (
    <span className={cn(
      'inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium',
      colorMap[variant] || colorMap.default,
      className,
    )}>
      {children}
    </span>
  )
}
