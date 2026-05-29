# Crextio kit

Warm editorial component kit — cream + mustard palette, 24px radius, soft shadows. Sister to `web/src/components/ui/shadcn/` but with a deliberately different aesthetic: it reads as **premium and friendly** rather than canonical-shadcn neutral. The reference is [crextio-dashboard.html](../../../../../crextio-dashboard.html) at the repo root.

Use it when you want a dashboard surface that doesn't look like every other admin panel — billing dashboards, "today at a glance" widgets, marketing pages, candidate-facing surfaces.

## What's in the box

| Component | Use it for |
|---|---|
| `WarmCard` | Base surface. `variant="cream"` (default) or `"dark"` for contrast cards |
| `WarmCardHeader` | Title row with optional right-side action slot |
| `WarmCardArrow` | The little circular "↗" button that opens the underlying surface |
| `MetricBar` | Slim horizontal % bar with diagonal-hatched empty fill |
| `HeadlineMetric` | Big serif number + icon + caption (stats row) |
| `VerticalBarChart` | 7-bar weekly chart with optional accent bar + floating tooltip |
| `SegmentedProgress` | 3-segment pill row (accent / dark / hatched-empty variants) |
| `TimerRing` | SVG circular progress ring with center label |
| `TaskList` + `TaskItem` | Checklist with done/pending status circles. Renders well on either dark or cream surfaces |
| `CalendarWeek` + `StackedAvatar` | Week timeline with absolutely-placed event blocks |

All components are typed, `forwardRef`'d, and accept `className` overrides. Imports come from a single barrel:

```ts
import { WarmCard, MetricBar, TimerRing } from '@/components/ui/crextio'
```

## Typography

The kit uses `font-serif` for display moments. Out of the box that resolves to whatever serif is configured in `tailwind.config.js` (currently nothing — Tailwind's default). For the full Crextio aesthetic, add Fraunces to `web/index.html`:

```html
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=Fraunces:opsz,wght@9..144,300;9..144,400;9..144,500&display=swap" rel="stylesheet">
```

…and either set `theme.extend.fontFamily.serif = ['Fraunces', 'ui-serif', 'serif']` in `tailwind.config.js`, or pass `style={{ fontFamily: 'Fraunces, serif' }}` on the surfaces that need it. Components degrade gracefully if Fraunces isn't loaded.

## Quick examples

### A "today" widget

```tsx
import { WarmCard, WarmCardHeader, WarmCardArrow, TimerRing } from '@/components/ui/crextio'

<WarmCard className="items-center">
  <WarmCardHeader title="Time tracker" action={<WarmCardArrow />} className="w-full" />
  <TimerRing value={75} label="02:35" sublabel="Work Time" />
</WarmCard>
```

### A bar-chart card

```tsx
import { WarmCard, WarmCardHeader, WarmCardArrow, VerticalBarChart } from '@/components/ui/crextio'

<WarmCard>
  <WarmCardHeader title="Progress" action={<WarmCardArrow />} />
  <div className="mb-3 flex items-end gap-3">
    <span className="font-serif text-[44px] font-light leading-[0.95] tracking-[-0.04em]">6.1<span className="text-[30px] italic text-[#8C8273]"> h</span></span>
    <span className="pb-1 text-[11.5px] leading-[1.4] text-[#8C8273]">Work Time<br />this week</span>
  </div>
  <VerticalBarChart
    data={[
      { label: 'S', value: 28 },
      { label: 'M', value: 48 },
      { label: 'T', value: 38 },
      { label: 'W', value: 56 },
      { label: 'T', value: 92, accent: true, tooltip: '5h 23m' },
      { label: 'F', value: 64 },
      { label: 'S', value: 22 },
    ]}
  />
</WarmCard>
```

### A dark task card

```tsx
import { WarmCard, WarmCardHeader, TaskList, TaskItem } from '@/components/ui/crextio'
import { Monitor, Zap } from 'lucide-react'

<WarmCard variant="dark">
  <WarmCardHeader
    title="Onboarding Task"
    action={<span className="font-serif text-[30px] font-light text-white">2/8</span>}
  />
  <TaskList>
    <TaskItem icon={<Monitor className="size-4" />} title="Interview"    time="Sep 13, 08:30" status="done" />
    <TaskItem icon={<Zap     className="size-4" />} title="Team Meeting" time="Sep 13, 10:30" status="done" />
    <TaskItem icon={<Zap     className="size-4" />} title="Project Update" time="Sep 13, 13:00" status="pending" />
  </TaskList>
</WarmCard>
```

### A week-view calendar

```tsx
import { WarmCard, CalendarWeek, StackedAvatar } from '@/components/ui/crextio'

<WarmCard className="h-[280px]">
  <CalendarWeek
    monthLabel={<>September <em className="not-italic text-[#D9A422]">2024</em></>}
    prevMonth="August"
    nextMonth="October"
    days={[
      { label: 'Mon', date: 22 },
      { label: 'Tue', date: 23 },
      { label: 'Wed', date: 24, today: true },
      { label: 'Thu', date: 25 },
      { label: 'Fri', date: 26 },
      { label: 'Sat', date: 27 },
    ]}
    hours={['8:00 am', '9:00 am', '10:00 am', '11:00 am']}
    events={[
      {
        id: 'sync',
        title: 'Weekly Team Sync',
        sub: 'Discuss progress on projects',
        day: 2, startHour: 1, daySpan: 2, hourSpan: 2,
        variant: 'dark',
        avatars: <><StackedAvatar tone="a">L</StackedAvatar><StackedAvatar tone="b">M</StackedAvatar><StackedAvatar tone="c">+3</StackedAvatar></>,
      },
      {
        id: 'onb',
        title: 'Onboarding Session',
        sub: 'Introduction for new hires',
        day: 4, startHour: 3, hourSpan: 2,
        variant: 'light',
        avatars: <><StackedAvatar tone="d">N</StackedAvatar><StackedAvatar tone="e">R</StackedAvatar></>,
      },
    ]}
  />
</WarmCard>
```

## Notes & gotchas

- **Colors are baked-in hex values.** The kit doesn't read from `--background` / `--primary` / shadcn tokens because the warm palette is the whole point. If you want a component to follow the project theme instead, pass a `className` that overrides — every component accepts `className` and uses `cn()` so `twMerge` resolves correctly.
- **`TaskList` propagates `data-surface`** to its children via CSS attribute selectors. You can also pass `surface` directly on a `TaskItem` if it lives outside a `TaskList`.
- **No global CSS or font side-effects.** Importing from the kit adds zero styles to the page until a component renders. Fraunces is recommended but not required.
- **RTL.** The hatched pattern and the calendar grid columns are direction-agnostic. Stacked avatars use negative `margin-right` — flip via `dir="rtl"` on a parent or pass `className="!-ml-1.5 !mr-0"` if your project's RTL flow needs it.
- **Dark-mode in the existing DMS app** — the components don't react to the project's `.dark` class. If you drop a `<WarmCard variant="cream">` into a dark-themed page it'll stand out (which is sometimes what you want). The `dark` variant is for the in-kit contrast — not for project dark mode.
- **Animation.** The reference HTML has a staggered fade-in on load. That's intentionally not in these components — too opinionated for a kit. Wrap your composed surface in framer-motion or CSS animation at the call site if you want it.

## Future additions worth considering

- `AccordionItem` matching the cream-card accordion from the reference
- `PillNav` for the active-pill nav element
- `GlassPill` for the `$1,200` salary pill (backdrop-blur over a photo)
- `AmbientBackground` for the radial-gradient + noise-overlay atmosphere

None of these are urgent — the eight above cover the cards you'd build in 90% of warm-aesthetic dashboards.
