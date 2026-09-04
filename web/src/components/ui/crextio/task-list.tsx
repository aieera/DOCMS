import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import { cn } from '@/lib/cn'

// Dark checklist from the "Onboarding Task" card. Designed to live
// inside a dark WarmCard but the surface= prop swaps to a cream
// variant if the host needs a light version.

export interface TaskListProps extends HTMLAttributes<HTMLUListElement> {
  surface?: 'dark' | 'cream'
}

export const TaskList = forwardRef<HTMLUListElement, TaskListProps>(
  ({ className, surface = 'dark', ...props }, ref) => (
    <ul
      ref={ref}
      data-surface={surface}
      className={cn('flex flex-col gap-1 overflow-hidden', className)}
      {...props}
    />
  ),
)
TaskList.displayName = 'TaskList'

export interface TaskItemProps extends Omit<HTMLAttributes<HTMLLIElement>, 'title'> {
  /** Small icon (16-18px) shown in the leading square */
  icon?: ReactNode
  /** Task title */
  title: ReactNode
  /** Subtitle / timestamp under the title */
  time?: ReactNode
  /** Status circle on the right edge */
  status?: 'done' | 'pending'
  /** Override the surface — by default inherits from the parent TaskList via data-surface */
  surface?: 'dark' | 'cream'
}

export const TaskItem = forwardRef<HTMLLIElement, TaskItemProps>(
  ({ className, icon, title, time, status = 'pending', surface, ...props }, ref) => {
    const isDone = status === 'done'
    // surface inherited from the parent TaskList via CSS attribute
    // selector so callers can mix dark + cream without prop-drilling.
    return (
      <li
        ref={ref}
        data-status={status}
        data-surface={surface}
        className={cn(
          'group/task grid grid-cols-[32px_1fr_22px] items-center gap-3 py-[7px]',
          'border-b last:border-b-0',
          '[[data-surface=dark]_&]:border-white/[0.06]',
          '[[data-surface=cream]_&]:border-border',
          surface === 'dark' && 'border-white/[0.06]',
          surface === 'cream' && 'border-border',
          className,
        )}
        {...props}
      >
        <span
          aria-hidden
          className={cn(
            'inline-flex h-8 w-8 items-center justify-center rounded-[10px]',
            '[[data-surface=dark]_&]:bg-white/[0.06] [[data-surface=dark]_&]:text-[#FAFAFA]',
            '[[data-surface=cream]_&]:bg-muted [[data-surface=cream]_&]:text-foreground',
          )}
        >
          {icon}
        </span>
        <div className="flex min-w-0 flex-col gap-px">
          <span
            className={cn(
              'truncate text-[13px] font-medium',
              isDone && cn(
                'line-through decoration-current/30',
                '[[data-surface=dark]_&]:text-[#8A8175]',
                '[[data-surface=cream]_&]:text-muted-foreground',
              ),
            )}
          >
            {title}
          </span>
          {time && (
            <span
              className={cn(
                'font-mono text-[10.5px] tracking-[0.02em]',
                '[[data-surface=dark]_&]:text-[#8A8175]',
                '[[data-surface=cream]_&]:text-muted-foreground',
              )}
            >
              {time}
            </span>
          )}
        </div>
        <span
          className={cn(
            'inline-flex h-[22px] w-[22px] items-center justify-center rounded-full',
            isDone
              ? 'bg-primary text-primary-foreground shadow-neu-sm'
              : cn(
                  'border border-dashed',
                  '[[data-surface=dark]_&]:border-white/20',
                  '[[data-surface=cream]_&]:border-border',
                ),
          )}
          aria-label={isDone ? 'completed' : 'pending'}
        >
          {isDone && (
            <svg width={11} height={11} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={3} strokeLinecap="round" strokeLinejoin="round">
              <polyline points="5 13 10 18 19 7" />
            </svg>
          )}
        </span>
      </li>
    )
  },
)
TaskItem.displayName = 'TaskItem'
