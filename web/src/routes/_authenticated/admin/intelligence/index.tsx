import { createFileRoute, Link } from '@tanstack/react-router'
import {
  Activity, AlertTriangle, Brain, FileSearch, KeyRound, Languages, MapPinned,
  Shield, ShieldAlert, Sparkles, Tags as TagsIcon, type LucideIcon,
} from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { cn } from '@/lib/cn'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

// /admin/intelligence is an intermediate breadcrumb path. Every
// child (auto-tag, ocr-review, models, …) appears in admin/index.tsx
// already, but the parent path itself rendered 404 when a user
// clicked the "Intelligence" crumb. This hub mirrors the INTEL_GROUP
// section of the admin home so the breadcrumb destination is a real,
// navigable page rather than dead-air.
//
// Cards stay in sync with admin/index.tsx by intention — small enough
// (13 entries) that duplication is cheaper than abstracting. If a
// new intelligence sub-route is added, mirror it in both places.
interface Section {
  to: string
  icon: LucideIcon
  label: string
  desc: string
}

const SECTIONS: Section[] = [
  { to: '/admin/intelligence/auto-tag',           icon: Sparkles,     label: 'Auto-tag config',     desc: 'Thresholds + weights for tag suggestions' },
  { to: '/admin/intelligence/tag-review',         icon: TagsIcon,     label: 'Tag review queue',    desc: 'Tenant-wide pending tag suggestions' },
  { to: '/admin/intelligence/routing-rules',      icon: MapPinned,    label: 'Routing rules',       desc: 'Smart-routing rules + config' },
  { to: '/admin/intelligence/filing-analytics',   icon: FileSearch,   label: 'Filing analytics',    desc: 'Suggestion acceptance + filing patterns' },
  { to: '/admin/intelligence/compliance',         icon: ShieldAlert,  label: 'PII / PHI findings',  desc: 'Open compliance findings queue' },
  { to: '/admin/intelligence/compliance-config',  icon: Shield,       label: 'Compliance config',   desc: 'PHI opt-in, custom patterns, thresholds' },
  { to: '/admin/intelligence/ocr-review',         icon: FileSearch,   label: 'OCR review queue',    desc: 'Pages flagged by OCR-quality scoring' },
  { to: '/admin/intelligence/ocr-config',         icon: FileSearch,   label: 'OCR quality config',  desc: 'Scoring thresholds, auto-retry, notify-on-poor' },
  { to: '/admin/intelligence/anomalies',          icon: AlertTriangle, label: 'Anomaly reports',    desc: 'Workspace outlier scans' },
  { to: '/admin/intelligence/models',             icon: Brain,        label: 'Model registry',      desc: 'Per-tenant fine-tuned classifiers' },
  { to: '/admin/intelligence/ner-config',         icon: Languages,    label: 'NER configuration',   desc: 'LLM tier toggle + per-tenant API key' },
  { to: '/admin/intelligence/usage',              icon: Activity,     label: 'LLM usage',           desc: 'Per-tenant token + cost tally across all models' },
  { to: '/admin/tenant/ai',                       icon: KeyRound,     label: 'AI provider',         desc: 'Provider, model, fallback, API key, budget' },
]

function IntelligenceHubPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Intelligence"
        description="Auto-tag, smart routing, compliance findings, OCR review, and the model + LLM registries."
      />
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {SECTIONS.map((s) => (
          <SectionCard key={s.to} section={s} />
        ))}
      </div>
    </div>
  )
}

function SectionCard({ section: { to, icon: Icon, label, desc } }: { section: Section }) {
  return (
    <Link
      to={to}
      className={cn(
        'block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
      )}
    >
      <Card className="group flex h-full items-start gap-3 p-4 transition-colors hover:bg-accent">
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-muted text-foreground transition-colors group-hover:bg-foreground group-hover:text-background">
          <Icon className="h-[1.05rem] w-[1.05rem]" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="flex items-center gap-1 truncate text-sm font-medium">
            {label}
            <DirectionalIcon name="ChevronRight" className="h-3.5 w-3.5 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
          </p>
          <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">{desc}</p>
        </div>
      </Card>
    </Link>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/')({
  component: IntelligenceHubPage,
})
