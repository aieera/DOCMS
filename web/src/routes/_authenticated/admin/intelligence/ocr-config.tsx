import { useEffect, useState } from 'react'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Save, AlertTriangle, ShieldAlert } from 'lucide-react'

import {
  getOcrQualityConfig,
  updateOcrQualityConfig,
  type OcrQualityConfig,
} from '@/api/ocr-quality'
import { getOcrEngineConfig, updateOcrEngineConfig, type OcrEngine } from '@/api/ocr'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useAuthStore } from '@/store/authStore'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'

// Backend (services/document/internal/handler/ocr_quality_handler.go)
// requires role owner|admin to PUT the config; owner|admin|
// compliance_officer can GET it. Mirror exactly — never offer the
// save affordance to a role the backend would reject.
const VIEW_ROLES = ['owner', 'admin', 'compliance_officer'] as const
const EDIT_ROLES = ['owner', 'admin'] as const

// Backend defaults (services/document/internal/handler/ocr_quality_handler.go
// defaultOCRConfigDTO) — used as the placeholder when no config has been
// written yet. The GET endpoint returns these for tenants that have
// never saved.
const DEFAULTS: OcrQualityConfig = {
  enabled: true,
  review_threshold: 0.60,
  excellent_threshold: 0.90,
  good_threshold: 0.75,
  fair_threshold: 0.60,
  auto_retry_below: 0.40,
  notify_on_poor: false,
}

function pct(n: number) {
  return Math.round(n * 100)
}

function frac(n: number) {
  return Math.max(0, Math.min(1, n / 100))
}

export function OcrConfigPage() {
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role) ?? ''
  const canView = (VIEW_ROLES as readonly string[]).includes(role)
  const canEdit = (EDIT_ROLES as readonly string[]).includes(role)

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['ocr-quality-config'],
    queryFn: getOcrQualityConfig,
    enabled: canView,
  })

  const [draft, setDraft] = useState<OcrQualityConfig | null>(null)
  useEffect(() => {
    if (data && !draft) setDraft(data)
  }, [data, draft])

  const save = useAppMutation({
    mutationFn: (patch: Partial<OcrQualityConfig>) => updateOcrQualityConfig(patch),
    onSuccess: (saved) => {
      qc.setQueryData(['ocr-quality-config'], saved)
      setDraft(saved)
      toast.success('OCR quality config saved')
    },
    defaultErrorMessage: 'Could not save OCR quality config',
  })

  // Permission gate (admin-only surface). Compliance officers can VIEW
  // (read-only); everyone else gets bounced.
  if (!canView) {
    return (
      <div className="max-w-3xl">
        <PageHeader variant="section"
          title="OCR quality config"
          description="Per-tenant scoring thresholds, auto-retry, and notifications."
        />
        <div className="mt-6 flex items-start gap-3 rounded-lg border border-amber-500/40 bg-amber-500/5 p-4 text-sm">
          <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
          <div>
            <p className="font-medium">Admin access required</p>
            <p className="mt-0.5 text-muted-foreground">
              This page is restricted to workspace owners, admins, and compliance officers.
            </p>
          </div>
        </div>
      </div>
    )
  }

  if (isError) {
    return (
      <div className="max-w-3xl">
        <PageHeader variant="section" title="OCR quality config" />
        <div className="mt-6 flex items-start gap-3 rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
          <div>
            <p className="font-medium text-destructive">Could not load OCR config</p>
            <p className="mt-0.5 text-muted-foreground">
              {(error as Error)?.message ?? 'Unknown error. Try refreshing.'}
            </p>
          </div>
        </div>
      </div>
    )
  }

  if (isLoading || !draft) {
    return (
      <div className="max-w-3xl">
        <PageHeader variant="section" title="OCR quality config" description="Loading current configuration…" />
        <div className="mt-6 space-y-3">
          {Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} className="h-12 w-full" />)}
        </div>
      </div>
    )
  }

  // HARD ordering — the document service (ocr_quality_repo.go) REJECTS any
  // save that doesn't satisfy excellent >= good >= fair, so enforce the same
  // rule here and block the save instead of warning "the backend will accept
  // this" and then having the request hard-fail. Uses >= (equality allowed)
  // to match the backend exactly.
  const orderingErrors: string[] = []
  if (draft.excellent_threshold < draft.good_threshold) {
    orderingErrors.push('Excellent threshold must be ≥ Good.')
  }
  if (draft.good_threshold < draft.fair_threshold) {
    orderingErrors.push('Good threshold must be ≥ Fair.')
  }

  // SOFT advisories — the backend does NOT enforce these (only excellent ≥
  // good ≥ fair), so we surface them but still allow the save.
  const warnings: string[] = []
  if (draft.fair_threshold <= draft.auto_retry_below) {
    warnings.push('Fair threshold should be greater than Auto-retry below.')
  }
  if (draft.review_threshold > draft.excellent_threshold) {
    warnings.push('Review threshold is higher than Excellent — every page will be flagged.')
  }

  // 0..1 range guards — hard stop because the backend stores fractions
  // (services/intelligence/app/tasks/ocr_quality.py compares against
  // these directly). Outside this range the behavior is undefined.
  const outOfRange =
    [draft.review_threshold, draft.excellent_threshold, draft.good_threshold,
     draft.fair_threshold, draft.auto_retry_below]
      .some((v) => v < 0 || v > 1)

  const dirty = JSON.stringify(draft) !== JSON.stringify(data)

  const onSave = () => {
    if (outOfRange) {
      toast.error('All thresholds must be between 0% and 100%')
      return
    }
    if (orderingErrors.length > 0) {
      toast.error('Thresholds must satisfy excellent ≥ good ≥ fair.')
      return
    }
    save.mutate(draft)
  }

  return (
    <div className="max-w-3xl">
      <PageHeader variant="section"
        title="OCR quality config"
        description="Per-tenant scoring thresholds, auto-retry, and review-queue routing."
      />

      {!canEdit && (
        <div className="mt-6 flex items-start gap-3 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-sm" data-testid="ocr-config-readonly-notice">
          <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
          <div>
            <p className="font-medium">View-only</p>
            <p className="text-muted-foreground">
              You can see the current configuration but only owners and admins can change it.
            </p>
          </div>
        </div>
      )}

      <EngineDefaultCard canEdit={canEdit} />

      <div className="mt-6 space-y-6 rounded-lg border border-border bg-card p-5" data-testid="ocr-config-form">
        <Toggle
          label="Score OCR quality on upload"
          help="When off, every page is treated as 'fair' and no review-queue routing happens."
          checked={draft.enabled}
          disabled={!canEdit || save.isPending}
          onChange={(v) => setDraft({ ...draft, enabled: v })}
        />

        <ThresholdRow
          label="Review threshold"
          help="Per-page composite below this routes the page to the review queue."
          value={draft.review_threshold}
          disabled={!canEdit || save.isPending}
          onChange={(v) => setDraft({ ...draft, review_threshold: v })}
          testid="threshold-review"
        />

        <div className="border-t border-border pt-5">
          <h3 className="text-sm font-semibold">Grade boundaries</h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Document-level grade is decided from the page-average score. Defaults: excellent {pct(DEFAULTS.excellent_threshold)}%, good {pct(DEFAULTS.good_threshold)}%, fair {pct(DEFAULTS.fair_threshold)}%.
          </p>
          <div className="mt-3 space-y-4">
            <ThresholdRow
              label="Excellent at or above"
              value={draft.excellent_threshold}
              disabled={!canEdit || save.isPending}
              onChange={(v) => setDraft({ ...draft, excellent_threshold: v })}
              testid="threshold-excellent"
            />
            <ThresholdRow
              label="Good at or above"
              value={draft.good_threshold}
              disabled={!canEdit || save.isPending}
              onChange={(v) => setDraft({ ...draft, good_threshold: v })}
              testid="threshold-good"
            />
            <ThresholdRow
              label="Fair at or above"
              help="Anything below Fair is graded 'poor'."
              value={draft.fair_threshold}
              disabled={!canEdit || save.isPending}
              onChange={(v) => setDraft({ ...draft, fair_threshold: v })}
              testid="threshold-fair"
            />
          </div>
        </div>

        <div className="border-t border-border pt-5">
          <h3 className="text-sm font-semibold">Recovery</h3>
          <div className="mt-3 space-y-4">
            <ThresholdRow
              label="Auto-retry OCR below"
              help="When the page-average score falls under this value, OCR is automatically re-queued with the fallback engine."
              value={draft.auto_retry_below}
              disabled={!canEdit || save.isPending}
              onChange={(v) => setDraft({ ...draft, auto_retry_below: v })}
              testid="threshold-auto-retry"
            />
            <Toggle
              label="Notify on poor quality"
              help="Emits dms.notify.ocr_poor.v1 to the tenant's configured notification channels."
              checked={draft.notify_on_poor}
              disabled={!canEdit || save.isPending}
              onChange={(v) => setDraft({ ...draft, notify_on_poor: v })}
            />
          </div>
        </div>

        {orderingErrors.length > 0 && (
          <div className="flex items-start gap-3 rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm" data-testid="ocr-config-errors">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
            <div>
              <p className="font-medium text-destructive">Invalid threshold ordering</p>
              <ul className="mt-1 list-disc space-y-0.5 ps-4 text-muted-foreground">
                {orderingErrors.map((e) => <li key={e}>{e}</li>)}
              </ul>
              <p className="mt-2 text-xs text-muted-foreground">
                Saving is blocked until excellent ≥ good ≥ fair — the backend rejects other orderings.
              </p>
            </div>
          </div>
        )}

        {warnings.length > 0 && (
          <div className="flex items-start gap-3 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-sm" data-testid="ocr-config-warnings">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
            <div>
              <p className="font-medium">Threshold ordering looks unusual</p>
              <ul className="mt-1 list-disc space-y-0.5 ps-4 text-muted-foreground">
                {warnings.map((w) => <li key={w}>{w}</li>)}
              </ul>
              <p className="mt-2 text-xs text-muted-foreground">
                The backend will still accept this — these are advisory.
              </p>
            </div>
          </div>
        )}

        {canEdit && (
          <div className="flex justify-end gap-2 border-t border-border pt-4">
            <Button
              variant="ghost"
              onClick={() => data && setDraft(data)}
              disabled={!dirty || save.isPending}
              data-testid="ocr-config-revert"
            >
              Revert
            </Button>
            <Button
              onClick={onSave}
              disabled={!dirty || save.isPending || outOfRange || orderingErrors.length > 0}
              loading={save.isPending}
              data-testid="ocr-config-save"
            >
              <Save className="me-2 h-4 w-4" /> Save
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}

// --- Sub-components ---------------------------------------------------------

function Toggle({
  label, help, checked, disabled, onChange,
}: {
  label: string
  help?: string
  checked: boolean
  disabled?: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <div className="flex items-start justify-between gap-4 text-sm">
      <div>
        <div className="font-medium">{label}</div>
        {help && <p className="mt-1 text-xs text-muted-foreground">{help}</p>}
      </div>
      <input
        type="checkbox"
        className="mt-1 h-4 w-4 accent-primary"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
    </div>
  )
}

function ThresholdRow({
  label, help, value, disabled, onChange, testid,
}: {
  label: string
  help?: string
  value: number
  disabled?: boolean
  onChange: (fraction: number) => void
  testid: string
}) {
  // Edit as integer percent for usability; store as 0..1 fraction.
  const percent = pct(value)
  const outOfRange = value < 0 || value > 1

  return (
    <div className="grid grid-cols-[1fr_auto] items-start gap-4 text-sm" data-testid={testid}>
      <div>
        <label htmlFor={testid} className="font-medium">{label}</label>
        {help && <p className="mt-1 text-xs text-muted-foreground">{help}</p>}
      </div>
      <div className="flex items-center gap-2">
        <input
          id={testid}
          type="number"
          min={0}
          max={100}
          step={1}
          value={Number.isFinite(percent) ? percent : 0}
          disabled={disabled}
          onChange={(e) => {
            const raw = Number(e.target.value)
            if (Number.isNaN(raw)) return
            onChange(frac(raw))
          }}
          className={`w-20 rounded-md border bg-background px-2 py-1 text-end text-sm ${outOfRange ? 'border-destructive' : 'border-border'}`}
        />
        <span className="text-xs text-muted-foreground">%</span>
      </div>
    </div>
  )
}

// Per-tenant default OCR engine (auto/printed/handwriting). Self-contained:
// reads/writes the intelligence service's ocr_config:{tenant} via /api/v1/
// intelligence/ocr/engine-config — the same key the OCR worker resolves
// against, so a change here re-routes future OCR jobs.
const ENGINES: { value: OcrEngine; label: string; help: string }[] = [
  { value: 'auto', label: 'Auto', help: 'Printed OCR; pages below the handwriting floor are re-tried with ICR and merged.' },
  { value: 'printed', label: 'Printed', help: 'Surya/Paddle only — fastest, best for typed documents.' },
  { value: 'handwriting', label: 'Handwriting (ICR)', help: 'TrOCR for ink, merged with printed OCR — best for forms & notes.' },
]

function EngineDefaultCard({ canEdit }: { canEdit: boolean }) {
  const qc = useQueryClient()
  const { data } = useQuery({ queryKey: ['ocr-engine-config'], queryFn: getOcrEngineConfig })
  const [engine, setEngine] = useState<OcrEngine | null>(null)
  useEffect(() => {
    if (data && engine === null) setEngine(data.engine)
  }, [data, engine])

  const save = useAppMutation({
    mutationFn: (next: OcrEngine) =>
      updateOcrEngineConfig({ engine: next, doc_type_overrides: data?.doc_type_overrides ?? {} }),
    onSuccess: (saved) => {
      qc.setQueryData(['ocr-engine-config'], saved)
      setEngine(saved.engine)
      toast.success('Default OCR engine saved')
    },
    defaultErrorMessage: 'Could not save OCR engine',
  })

  const current = engine ?? data?.engine ?? 'auto'
  return (
    <div className="mt-6 space-y-3 rounded-lg border border-border bg-card p-5" data-testid="ocr-engine-config">
      <div>
        <h3 className="text-sm font-semibold">Default OCR engine</h3>
        <p className="mt-0.5 text-xs text-muted-foreground">
          Applied to new uploads when no per-document engine is forced. Handwriting routes through
          TrOCR (ICR); the engine that actually ran is recorded on every page and the OCR-completed event.
        </p>
      </div>
      <div className="grid gap-2 sm:grid-cols-3">
        {ENGINES.map((e) => (
          <button
            key={e.value}
            type="button"
            disabled={!canEdit || save.isPending}
            onClick={() => { setEngine(e.value); save.mutate(e.value) }}
            className={`rounded-md border p-3 text-start text-sm transition-colors disabled:opacity-60 ${
              current === e.value ? 'border-primary bg-primary/5' : 'border-border hover:bg-muted/40'
            }`}
            data-testid={`ocr-engine-${e.value}`}
          >
            <p className="font-medium">{e.label}</p>
            <p className="mt-0.5 text-xs text-muted-foreground">{e.help}</p>
          </button>
        ))}
      </div>
    </div>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/ocr?tab=config). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/intelligence/ocr-config')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/ocr', search: { tab: 'config' }, replace: true })
  },
})
