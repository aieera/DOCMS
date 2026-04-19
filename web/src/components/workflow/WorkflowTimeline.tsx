import { cn } from '@/lib/cn'
import { CheckCircle2, Circle, Clock } from 'lucide-react'

interface Step { name: string; status: 'completed' | 'current' | 'pending'; assignee?: string }

export function WorkflowTimeline({ steps }: { steps: Step[] }) {
  return (
    <div className="space-y-0">
      {steps.map((s, i) => (
        <div key={i} className="flex gap-3">
          <div className="flex flex-col items-center">
            {s.status === 'completed' && <CheckCircle2 className="h-5 w-5 text-emerald-500" />}
            {s.status === 'current' && <Clock className="h-5 w-5 text-[var(--color-primary)]" />}
            {s.status === 'pending' && <Circle className="h-5 w-5 text-slate-300 dark:text-slate-600" />}
            {i < steps.length - 1 && <div className={cn('w-0.5 flex-1 my-1', s.status === 'completed' ? 'bg-emerald-500' : 'bg-slate-200 dark:bg-slate-700')} />}
          </div>
          <div className="pb-4">
            <p className={cn('text-sm', s.status === 'current' && 'font-medium')}>{s.name}</p>
            {s.assignee && <p className="text-xs text-[var(--color-text-secondary)]">{s.assignee}</p>}
          </div>
        </div>
      ))}
    </div>
  )
}
