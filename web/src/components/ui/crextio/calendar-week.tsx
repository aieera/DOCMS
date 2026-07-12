import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import { cn } from '@/lib/cn'

export interface CalendarDay {
  /** Short weekday label, e.g. "Mon" */
  label: string
  /** Day-of-month number */
  date: number | string
  /** Highlight as the current day */
  today?: boolean
}

export interface CalendarEvent {
  /** Stable id for keying */
  id: string
  title: ReactNode
  sub?: ReactNode
  /** 1-based index into the days array */
  day: number
  /** 1-based index into the hours array */
  startHour: number
  /** How many days the event spans (default 1) */
  daySpan?: number
  /** How many hour rows the event spans (default 1) */
  hourSpan?: number
  /** dark = charcoal block, light = cream block */
  variant?: 'dark' | 'light'
  /** Trailing avatar stack (use the <Avatar> primitive or any element) */
  avatars?: ReactNode
}

export interface CalendarWeekProps extends HTMLAttributes<HTMLDivElement> {
  /** e.g. "September 2024" — accent emphasized via a child <em> if desired */
  monthLabel: ReactNode
  /** Optional previous/next month chips */
  prevMonth?: ReactNode
  nextMonth?: ReactNode
  days: CalendarDay[]
  /** Left rail labels, top-to-bottom (e.g. ["8:00 am", "9:00 am", ...]) */
  hours: string[]
  events: CalendarEvent[]
}

// Week timeline with absolutely-placed events. Rows = hour slots,
// columns = days. Pure CSS grid — events are children of the grid
// with explicit grid-column / grid-row so they snap to the timeline.

export const CalendarWeek = forwardRef<HTMLDivElement, CalendarWeekProps>(
  ({ className, monthLabel, prevMonth, nextMonth, days, hours, events, ...props }, ref) => {
    const cols = `56px repeat(${days.length}, minmax(0, 1fr))`
    return (
      <div ref={ref} className={cn('flex flex-1 flex-col', className)} {...props}>
        {/* month switcher */}
        <header className="mb-2.5 flex items-baseline gap-3.5">
          {prevMonth && (
            <span className="text-[12.5px] font-medium text-[#B9AC95]">{prevMonth}</span>
          )}
          <span
            className="font-serif text-[18px] font-normal tracking-[-0.01em]"
            style={{ fontVariationSettings: '"opsz" 96' }}
          >
            {monthLabel}
          </span>
          {nextMonth && (
            <span className="ms-auto text-[12.5px] font-medium text-[#B9AC95]">{nextMonth}</span>
          )}
        </header>

        {/* day header row */}
        <div className="grid" style={{ gridTemplateColumns: cols }}>
          <span />
          {days.map((d, i) => (
            <div key={i} className="flex flex-col items-center gap-0.5 pb-3 text-[11px] text-[#8C8273]">
              <span>{d.label}</span>
              <span
                className={cn(
                  'font-serif text-[16px] font-normal tracking-[-0.01em]',
                  d.today ? 'text-[#D9A422]' : 'text-[#1A1A1A]',
                )}
                style={{ fontVariationSettings: '"opsz" 96' }}
              >
                {d.date}
              </span>
            </div>
          ))}
        </div>

        {/* timeline grid */}
        <div
          className="relative grid flex-1 border-t border-dashed border-[rgba(26,26,26,0.14)]"
          style={{
            gridTemplateColumns: cols,
            gridTemplateRows: `repeat(${hours.length}, minmax(38px, 1fr))`,
          }}
        >
          {/* hour rail */}
          {hours.map((h, i) => (
            <span
              key={`h-${i}`}
              className="border-b border-dashed border-[rgba(26,26,26,0.08)] pe-2 pt-1.5 text-end font-mono text-[10.5px] text-[#8C8273]"
              style={{ gridColumn: 1, gridRow: i + 1 }}
            >
              {h}
            </span>
          ))}

          {/* empty grid cells (for the dashed lines) */}
          {hours.map((_, hourIdx) =>
            days.map((_, dayIdx) => (
              <span
                key={`c-${hourIdx}-${dayIdx}`}
                className="border-b border-s border-dashed border-[rgba(26,26,26,0.08)]"
                style={{ gridColumn: dayIdx + 2, gridRow: hourIdx + 1 }}
              />
            )),
          )}

          {/* events */}
          {events.map((ev) => {
            const isDark = ev.variant !== 'light'
            return (
              <div
                key={ev.id}
                className={cn(
                  'relative m-0.5 mx-1 flex flex-col gap-0.5 overflow-hidden rounded-[14px] p-2.5',
                  'shadow-[0_2px_8px_-2px_rgba(80,60,10,0.10)]',
                  isDark ? 'bg-[#1A1A1A] text-[#FAFAFA]' : 'border border-[rgba(26,26,26,0.08)] bg-[#FBF1D9] text-[#1A1A1A]',
                )}
                style={{
                  gridColumn: `${ev.day + 1} / span ${ev.daySpan ?? 1}`,
                  gridRow: `${ev.startHour} / span ${ev.hourSpan ?? 1}`,
                }}
              >
                <span className="text-[12.5px] font-semibold leading-tight">{ev.title}</span>
                {ev.sub && (
                  <span className="text-[11px] leading-tight opacity-70">{ev.sub}</span>
                )}
                {ev.avatars && (
                  <div className={cn('mt-auto flex pt-1.5', isDark ? '[&_*]:!border-[#1A1A1A]' : '[&_*]:!border-[#FBF1D9]')}>
                    {ev.avatars}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </div>
    )
  },
)
CalendarWeek.displayName = 'CalendarWeek'

// Small avatar primitive matching the stacked overlapping circles
// in the event blocks. Use as event.avatars children.
export interface StackedAvatarProps extends HTMLAttributes<HTMLSpanElement> {
  children: ReactNode
  tone?: 'a' | 'b' | 'c' | 'd' | 'e'
}

const TONE: Record<NonNullable<StackedAvatarProps['tone']>, string> = {
  a: 'bg-gradient-to-br from-[#FFD58A] to-[#E89B1F]',
  b: 'bg-gradient-to-br from-[#C8B89A] to-[#8A7758]',
  c: 'bg-gradient-to-br from-[#FFE2A4] to-[#C99B3D]',
  d: 'bg-gradient-to-br from-[#F0E0B8] to-[#A88C58]',
  e: 'bg-gradient-to-br from-[#FFCC80] to-[#BA6E1E]',
}

export const StackedAvatar = forwardRef<HTMLSpanElement, StackedAvatarProps>(
  ({ className, children, tone = 'a', ...props }, ref) => (
    <span
      ref={ref}
      className={cn(
        'inline-flex h-[22px] w-[22px] -me-1.5 items-center justify-center rounded-full',
        'border-2 text-[10px] font-semibold text-[#1A1A1A]',
        TONE[tone],
        className,
      )}
      {...props}
    >
      {children}
    </span>
  ),
)
StackedAvatar.displayName = 'StackedAvatar'
