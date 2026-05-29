import { createFileRoute } from '@tanstack/react-router'
import {
  Bell,
  Briefcase,
  CalendarRange,
  ChevronDown,
  MoreHorizontal,
  Pause,
  Play,
  RefreshCw,
  ShieldCheck,
  Target,
  Users,
  Zap,
} from 'lucide-react'
import {
  WarmCard,
  WarmCardHeader,
  WarmCardArrow,
  MetricBar,
  HeadlineMetric,
  VerticalBarChart,
  SegmentedProgress,
  TimerRing,
  TaskList,
  TaskItem,
  CalendarWeek,
  StackedAvatar,
} from '@/components/ui/crextio'

// Live preview of the Crextio component kit
// (web/src/components/ui/crextio). Composes all 8 primitives into the
// reference HR dashboard from crextio-dashboard.html so the warm
// editorial aesthetic can be verified in the real app shell.
//
// Sandbox surface only — no production data, no API calls. Delete the
// route file to remove the page (the kit components stay).

function CrextioSandbox() {
  return (
    <div
      className="-m-4 min-h-[calc(100vh-8rem)] rounded-3xl p-6 md:-m-6 md:p-8"
      style={{
        background:
          'radial-gradient(80% 60% at 8% -10%, #FFF7C8 0%, transparent 60%),' +
          'radial-gradient(50% 50% at 100% -5%, #FFE19A 0%, transparent 55%),' +
          'radial-gradient(60% 50% at 110% 100%, #FFD988 0%, transparent 55%),' +
          'linear-gradient(140deg, #FFF8DE 0%, #FBE9B5 100%)',
      }}
    >
      <div className="mx-auto flex max-w-[1280px] flex-col gap-6">
        {/* Sandbox banner so it's obvious this isn't a production page */}
        <div className="self-start rounded-full border border-[rgba(26,26,26,0.12)] bg-white/60 px-3 py-1 font-mono text-[11px] tracking-wide text-[#8C8273] backdrop-blur">
          sandbox · crextio component kit preview · no live data
        </div>

        {/* ---- Header ---- */}
        <header className="grid gap-10 lg:grid-cols-[1.55fr_1fr]">
          <div>
            <h1
              className="mb-5 font-serif text-[42px] font-light leading-none tracking-[-0.035em] text-[#1A1A1A]"
              style={{ fontVariationSettings: '"opsz" 144' }}
            >
              Welcome in,{' '}
              <em className="font-normal italic">
                Nixtio<span className="not-italic text-[#D9A422]">.</span>
              </em>
            </h1>
            <div className="grid grid-cols-2 gap-3.5 sm:grid-cols-4">
              <MetricBar label="Interviews" value={15} variant="dark" />
              <MetricBar label="Hired" value={15} variant="accent" />
              <MetricBar label="Project time" value={60} variant="dark" />
              <MetricBar label="Output" value={10} variant="dark" />
            </div>
          </div>

          <div className="grid grid-cols-3 items-end gap-2">
            <HeadlineMetric
              icon={<Users className="h-3.5 w-3.5" />}
              value={78}
              label="Employee"
            />
            <HeadlineMetric
              icon={<Briefcase className="h-3.5 w-3.5" />}
              value={56}
              label="Hirings"
              withDivider
            />
            <HeadlineMetric
              icon={<CalendarRange className="h-3.5 w-3.5" />}
              value={203}
              label="Projects"
              withDivider
            />
          </div>
        </header>

        {/* ---- Main grid ---- */}
        <section className="grid gap-5 lg:grid-cols-[286px_minmax(0,1fr)_minmax(0,1fr)_302px] lg:grid-rows-[296px_268px]">

          {/* Profile card — bare so we can full-bleed the photo */}
          <WarmCard
            variant="bare"
            className="relative flex min-h-[296px] flex-col justify-end overflow-hidden rounded-[24px] text-white shadow-[0_8px_28px_-10px_rgba(80,60,10,0.18)] lg:row-start-1"
            style={{
              backgroundImage:
                'linear-gradient(180deg, transparent 50%, rgba(0,0,0,0.7) 100%),' +
                'url("https://images.unsplash.com/photo-1494790108377-be9c29b29330?w=800&h=1000&fit=crop&q=80")',
              backgroundSize: 'cover',
              backgroundPosition: 'center',
            }}
          >
            <div className="flex flex-col gap-0.5 p-5">
              <h3
                className="font-serif text-[22px] font-normal leading-tight tracking-[-0.015em]"
                style={{ fontVariationSettings: '"opsz" 96' }}
              >
                Lora Piterson
              </h3>
              <span className="text-[12.5px] text-white/75">UX/UI Designer</span>
            </div>
            <span className="absolute bottom-4 right-4 rounded-full border border-white/35 bg-white/[0.18] px-4 py-2 font-mono text-[13px] font-semibold text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.4)] backdrop-blur-md">
              $1,200
            </span>
          </WarmCard>

          {/* Progress */}
          <WarmCard className="lg:row-start-1">
            <WarmCardHeader title="Progress" action={<WarmCardArrow />} />
            <div className="mb-3 flex items-end gap-3">
              <span
                className="font-serif text-[44px] font-light leading-[0.95] tracking-[-0.04em]"
                style={{ fontVariationSettings: '"opsz" 144' }}
              >
                6.1
                <span className="text-[30px] italic text-[#8C8273]"> h</span>
              </span>
              <span className="pb-1 text-[11.5px] leading-[1.4] text-[#8C8273]">
                Work Time
                <br />
                this week
              </span>
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

          {/* Time tracker */}
          <WarmCard className="items-center lg:row-start-1">
            <WarmCardHeader
              title="Time tracker"
              action={<WarmCardArrow />}
              className="w-full"
            />
            <TimerRing value={75} label="02:35" sublabel="Work Time" />
            <div className="mt-auto flex items-center gap-2.5">
              <button
                type="button"
                aria-label="Play"
                className="inline-flex h-10 w-10 items-center justify-center rounded-full border border-[rgba(26,26,26,0.08)] bg-[#FBF1D9] text-[#1A1A1A] transition-colors hover:bg-[#FFEEC0]"
              >
                <Play className="h-4 w-4 fill-current" strokeWidth={0} />
              </button>
              <button
                type="button"
                aria-label="Pause"
                className="inline-flex h-10 w-10 items-center justify-center rounded-full border border-[rgba(26,26,26,0.08)] bg-[#FBF1D9] text-[#1A1A1A] transition-colors hover:bg-[#FFEEC0]"
              >
                <Pause className="h-4 w-4 fill-current" strokeWidth={0} />
              </button>
              <button
                type="button"
                aria-label="Set alarm"
                className="inline-flex h-10 w-10 items-center justify-center rounded-full bg-[#1A1A1A] text-white transition-colors hover:bg-[#2A2A2A]"
              >
                <Bell className="h-4 w-4" />
              </button>
            </div>
          </WarmCard>

          {/* Right column: Onboarding + Onboarding Task stacked spanning both rows */}
          <div className="flex min-h-0 flex-col gap-5 lg:col-start-4 lg:row-span-2 lg:row-start-1">
            <WarmCard className="flex-none">
              <WarmCardHeader
                title="Onboarding"
                action={
                  <span
                    className="font-serif text-[30px] font-light leading-none tracking-[-0.03em]"
                    style={{ fontVariationSettings: '"opsz" 144' }}
                  >
                    18
                    <small className="text-[18px] text-[#8C8273]">%</small>
                  </span>
                }
              />
              <SegmentedProgress
                segments={[
                  { label: '30%', variant: 'accent' },
                  { label: '25%', variant: 'dark' },
                  { label: '0%', variant: 'empty' },
                ]}
                caption="task"
              />
            </WarmCard>

            <WarmCard variant="dark" className="flex-1 min-h-0">
              <WarmCardHeader
                title="Onboarding Task"
                titleClassName="text-white"
                action={
                  <span
                    className="font-serif text-[30px] font-light leading-none tracking-[-0.03em] text-white"
                    style={{ fontVariationSettings: '"opsz" 144' }}
                  >
                    2/8
                  </span>
                }
              />
              <TaskList>
                <TaskItem
                  icon={<Briefcase className="h-4 w-4" />}
                  title="Interview"
                  time="Sep 13, 08:30"
                  status="done"
                />
                <TaskItem
                  icon={<Zap className="h-4 w-4" />}
                  title="Team Meeting"
                  time="Sep 13, 10:30"
                  status="done"
                />
                <TaskItem
                  icon={<RefreshCw className="h-4 w-4" />}
                  title="Project Update"
                  time="Sep 13, 13:00"
                  status="pending"
                />
                <TaskItem
                  icon={<Target className="h-4 w-4" />}
                  title="Discuss Q3 Goals"
                  time="Sep 13, 14:45"
                  status="pending"
                />
                <TaskItem
                  icon={<ShieldCheck className="h-4 w-4" />}
                  title="HR Policy Review"
                  time="Sep 13, 16:30"
                  status="pending"
                />
              </TaskList>
            </WarmCard>
          </div>

          {/* Accordion */}
          <WarmCard className="px-4 py-3 lg:col-start-1 lg:row-start-2">
            {[
              { label: 'Pension contributions' },
              { label: 'Devices', open: true, body: <DevicesPanel /> },
              { label: 'Compensation Summary' },
              { label: 'Employee Benefits' },
            ].map((s, i, arr) => (
              <details
                key={s.label}
                open={s.open}
                className={
                  i < arr.length - 1
                    ? 'border-b border-[rgba(26,26,26,0.08)] py-1'
                    : 'py-1'
                }
              >
                <summary className="flex cursor-pointer items-center justify-between gap-2 py-3 text-[13.5px] font-medium [&::-webkit-details-marker]:hidden">
                  {s.label}
                  <ChevronDown className="h-3.5 w-3.5 text-[#8C8273] transition-transform group-open:rotate-180 [details[open]_&]:rotate-180" />
                </summary>
                {s.body}
              </details>
            ))}
          </WarmCard>

          {/* Calendar */}
          <WarmCard className="px-4 pb-4 pt-3 lg:col-span-2 lg:col-start-2 lg:row-start-2">
            <CalendarWeek
              monthLabel={
                <>
                  September <em className="not-italic text-[#D9A422]">2024</em>
                </>
              }
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
                  day: 2,
                  startHour: 1,
                  daySpan: 2,
                  hourSpan: 2,
                  variant: 'dark',
                  avatars: (
                    <>
                      <StackedAvatar tone="a">L</StackedAvatar>
                      <StackedAvatar tone="b">M</StackedAvatar>
                      <StackedAvatar tone="c">+3</StackedAvatar>
                    </>
                  ),
                },
                {
                  id: 'onb',
                  title: 'Onboarding Session',
                  sub: 'Introduction for new hires',
                  day: 4,
                  startHour: 3,
                  hourSpan: 2,
                  variant: 'light',
                  avatars: (
                    <>
                      <StackedAvatar tone="d">N</StackedAvatar>
                      <StackedAvatar tone="e">R</StackedAvatar>
                    </>
                  ),
                },
              ]}
            />
          </WarmCard>
        </section>
      </div>
    </div>
  )
}

function DevicesPanel() {
  return (
    <div className="grid grid-cols-[46px_1fr_22px] items-center gap-3 py-1">
      <div
        className="relative h-[34px] w-[46px] overflow-hidden rounded-lg shadow-[inset_0_1px_0_rgba(255,255,255,0.5),0_1px_2px_rgba(0,0,0,0.08)]"
        style={{ background: 'linear-gradient(180deg, #E8DCC4 0%, #C9B999 100%)' }}
        aria-hidden
      >
        <span
          className="absolute inset-x-1 bottom-[5px] top-[3px] rounded"
          style={{ background: 'linear-gradient(135deg, #6E5E47, #3A302B)' }}
        />
        <span className="absolute bottom-px left-1/2 h-0.5 w-3 -translate-x-1/2 rounded bg-[#877864]" />
      </div>
      <div>
        <div className="text-[13px] font-medium leading-tight">MacBook Air</div>
        <div className="text-[11px] text-[#8C8273]">Version M1</div>
      </div>
      <button
        type="button"
        aria-label="More"
        className="inline-flex h-[26px] w-[26px] items-center justify-center rounded-full text-[#8C8273] transition-colors hover:bg-black/5 hover:text-[#1A1A1A]"
      >
        <MoreHorizontal className="h-3.5 w-3.5" />
      </button>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/sandbox/crextio')({
  component: CrextioSandbox,
})
