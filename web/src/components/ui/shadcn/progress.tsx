import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import * as ProgressPrimitive from '@radix-ui/react-progress'
import { useDirection } from '@/hooks/useDirection'
import { cn } from '@/lib/cn'

const Progress = forwardRef<
  ElementRef<typeof ProgressPrimitive.Root>,
  ComponentPropsWithoutRef<typeof ProgressPrimitive.Root>
>(({ className, value, ...props }, ref) => {
  const dir = useDirection()
  // A missing value reads as empty; Radix rejects anything outside 0-100.
  const pct = Math.min(100, Math.max(0, value ?? 0))
  const offset = 100 - pct
  return (
    <ProgressPrimitive.Root
      ref={ref}
      value={pct}
      className={cn('relative h-2 w-full overflow-hidden rounded-full bg-muted', className)}
      {...props}
    >
      <ProgressPrimitive.Indicator
        className="h-full w-full flex-1 bg-primary transition-all"
        // translateX is physical: the indicator slides in from the left under
        // LTR and has to slide in from the right under RTL, so the bar fills
        // from the inline start in both.
        style={{ transform: `translateX(${dir === 'rtl' ? offset : -offset}%)` }}
      />
    </ProgressPrimitive.Root>
  )
})
Progress.displayName = ProgressPrimitive.Root.displayName

export { Progress }
