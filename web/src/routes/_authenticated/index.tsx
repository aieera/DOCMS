import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Search, Sparkles, Upload, type LucideIcon } from 'lucide-react'
import { getWorkspaces } from '@/api/workspaces'
import { PendingSuggestionsCard } from '@/components/intelligence/PendingSuggestionsCard'
import { DashboardUploadDialog } from '@/components/documents/DashboardUploadDialog'
import { WarmCard } from '@/components/ui/crextio'
import { useAuthStore } from '@/store/authStore'
import { cn } from '@/lib/cn'
import { shortcutLabel } from '@/lib/platform'
import { DashboardHead } from '@/components/dashboard/DashboardHead'
import { DocumentsAddedChart } from '@/components/dashboard/DocumentsAddedChart'
import { FileTypesAndContributors } from '@/components/dashboard/FileTypesAndContributors'
import { KpiStrip } from '@/components/dashboard/KpiStrip'
import { LifecycleDonut } from '@/components/dashboard/LifecycleDonut'
import { NeedsAttention } from '@/components/dashboard/NeedsAttention'
import { greet } from '@/components/dashboard/greet'

function DashboardPage() {
  const { t } = useTranslation('common')
  const user = useAuthStore((s) => s.user)
  const greeting = greet(t, user?.display_name?.trim().split(/\s+/)[0])
  // Same query (and key) as KpiStrip's, so it is fetched once.
  const workspaces = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces, staleTime: 60_000 })
  // Upload dialog state lives here so BOTH triggers share one dialog:
  // the header button (always visible, unmistakable) and the Quick-
  // actions card (click or drop files onto it).
  const [uploadOpen, setUploadOpen] = useState(false)
  const [droppedFiles, setDroppedFiles] = useState<File[]>([])

  return (
    <div className="space-y-6">
      <DashboardHead
        greeting={greeting}
        workspaceCount={workspaces.isSuccess ? workspaces.data.length : undefined}
        onUpload={() => setUploadOpen(true)}
      />

      <KpiStrip />

      <PendingSuggestionsCard />

      {/* Trend beside composition: "how it's moving" next to "what I have". */}
      <div className="grid gap-6 lg:grid-cols-3">
        <DocumentsAddedChart className="lg:col-span-2" />
        <LifecycleDonut delayIndex={5} />
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <NeedsAttention delayIndex={6} />
        <FileTypesAndContributors />
      </div>

      <QuickActions
        onUploadClick={() => setUploadOpen(true)}
        onUploadDrop={(files) => {
          setDroppedFiles(files)
          setUploadOpen(true)
        }}
      />

      <DashboardUploadDialog
        open={uploadOpen}
        onOpenChange={(v) => {
          setUploadOpen(v)
          if (!v) setDroppedFiles([])
        }}
        initialFiles={droppedFiles}
      />
    </div>
  )
}

// ---- Quick actions -------------------------------------------------------

interface QuickAction {
  icon: LucideIcon
  // Keys, not copy: this array is module-level, evaluated once at import
  // time, so a literal here would be captured in whatever locale
  // happened to be active first and never re-render on a switch.
  labelKey: string
  // Either a navigation target or an in-place action. The Upload card
  // used to be a plain link to /workspaces — it "uploaded" nothing and
  // never asked for a destination. It now opens the smart upload dialog.
  href?: string
  action?: 'upload'
  descKey: string
  // Bare shortcut key. The Cmd-vs-Ctrl modifier is resolved at render
  // time from the viewer's platform (lib/platform.ts) — hard-coding ⌘
  // here told every Windows user the wrong key while the topbar chip
  // beside it correctly said Ctrl.
  shortcutKey?: string
}

const QUICK_ACTIONS: readonly QuickAction[] = [
  { icon: Search, labelKey: 'dashboard.qa.search_label', href: '/search', descKey: 'dashboard.qa.search_desc', shortcutKey: 'K' },
  { icon: Sparkles, labelKey: 'dashboard.qa.ask_label', href: '/ask', descKey: 'dashboard.qa.ask_desc' },
  { icon: Upload, labelKey: 'dashboard.qa.upload_label', action: 'upload', descKey: 'dashboard.qa.upload_desc' },
]

function QuickActions({
  onUploadClick,
  onUploadDrop,
}: {
  onUploadClick: () => void
  onUploadDrop: (files: File[]) => void
}) {
  const { t } = useTranslation('common')
  const cardBody = ({ icon: Icon, labelKey, descKey, shortcutKey, action }: QuickAction) => (
    <WarmCard
      padded="md"
      className={cn(
        'h-full transition-all group-hover:-translate-y-0.5 group-hover:border-primary/50 group-hover:shadow-neu',
        // The Upload card is an ACTION, not a navigation link — give it
        // a standing accent so it reads as the dashboard's upload
        // button rather than one more shortcut tile.
        action === 'upload' && 'border-primary/50 bg-primary/5 dark:bg-primary/10',
      )}
    >
      <div className="flex items-start gap-3">
        <span
          className="flex h-9 w-9 shrink-0 items-center justify-center rounded-[12px] bg-muted text-foreground transition-colors group-hover:bg-primary group-hover:text-primary-foreground"
          aria-hidden
        >
          <Icon className="h-[1.1rem] w-[1.1rem]" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center justify-between gap-2">
            <p className="text-sm font-medium">{t(labelKey)}</p>
            {shortcutKey && (
              <kbd className="pointer-events-none inline-flex h-5 shrink-0 select-none items-center gap-0.5 rounded border border-border bg-background px-1.5 font-mono text-[10px] font-medium text-foreground/70">
                {shortcutLabel(shortcutKey)}
              </kbd>
            )}
          </div>
          <p className="mt-0.5 text-xs text-muted-foreground">{t(descKey)}</p>
        </div>
      </div>
    </WarmCard>
  )

  const cardShell =
    'group block h-full w-full text-start focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background rounded-[24px]'

  return (
    <section aria-labelledby="quick-actions-heading">
      <h2 id="quick-actions-heading" className="mb-3 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        {t('dashboard.quick_actions')}
      </h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {QUICK_ACTIONS.map((qa) =>
          qa.action === 'upload' ? (
            <button
              key={qa.labelKey}
              type="button"
              className={cardShell}
              onClick={onUploadClick}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                e.preventDefault()
                const dropped = Array.from(e.dataTransfer.files ?? [])
                onUploadDrop(dropped)
              }}
              data-testid="quick-action-upload"
            >
              {cardBody(qa)}
            </button>
          ) : (
            <Link key={qa.labelKey} to={qa.href!} className={cardShell}>
              {cardBody(qa)}
            </Link>
          ),
        )}
      </div>
    </section>
  )
}

export const Route = createFileRoute('/_authenticated/')({ component: DashboardPage })
