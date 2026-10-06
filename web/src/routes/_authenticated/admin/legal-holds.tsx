import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '@/i18n'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Scale, ShieldOff } from 'lucide-react'

import { listHolds, releaseHold, type LegalHold } from '@/api/holds'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/shadcn/input'
import { Textarea } from '@/components/ui/shadcn/textarea'
import { Dialog } from '@/components/ui/Dialog'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatDate, formatRelativeTime } from '@/lib/formatters'
import { cn } from '@/lib/cn'

type StatusFilter = 'active' | 'released' | 'all'

export function LegalHoldsPage() {
  const { t } = useTranslation('common')
  const [filter, setFilter] = useState<StatusFilter>('active')
  const [pendingRelease, setPendingRelease] = useState<LegalHold | null>(null)
  const qc = useQueryClient()

  const statusParam = filter === 'all' ? undefined : filter
  const { data, isLoading } = useQuery({
    queryKey: ['legal-holds', filter],
    queryFn: () => listHolds(statusParam ? { status: statusParam } : {}),
  })

  const release = useAppMutation({
    mutationFn: ({ id, reason, approver }: { id: string; reason: string; approver: string }) =>
      releaseHold(id, { reason, approver_id: approver }),
    onSuccess: () => {
      toast.success(t('adm.hold.released_toast'))
      qc.invalidateQueries({ queryKey: ['legal-holds'] })
      setPendingRelease(null)
    },
    onError: (err: unknown) => toast.error(extractMessage(err)),
  })

  return (
    <div className="space-y-6">
      <PageHeader
        noMargin
        title={t('adm.hold.title')}
        description={t('adm.hold.description')}
      />

      <SegmentedFilter value={filter} onChange={setFilter} />

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-20" />)}
        </div>
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<Scale className="h-6 w-6" />}
          title={filter === 'active' ? t('adm.hold.empty_active') : t('adm.hold.empty_any')}
          description={t('adm.hold.empty_desc')}
        />
      ) : (
        <ul className="space-y-2">
          {data.map((hold) => (
            <li key={hold.id}>
              <Card className="flex flex-col gap-3 p-4 sm:flex-row sm:items-start sm:justify-between">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium">{hold.name}</span>
                    <Badge variant={hold.is_active ? 'in_review' : 'archived'}>
                      {hold.is_active ? t('adm.hold.badge_active') : t('adm.hold.badge_released')}
                    </Badge>
                    {hold.matter_reference && (
                      <span className="text-xs text-muted-foreground">{t('adm.hold.matter')} <code dir="ltr" className="rounded bg-muted px-1 py-0.5 font-mono">{hold.matter_reference}</code></span>
                    )}
                  </div>
                  {hold.description && (
                    <p className="mt-1 max-w-[85ch] text-sm text-muted-foreground">{hold.description}</p>
                  )}
                  {/* dir="auto" on the whole line: it opens with "Applied"
                      but the dates inside are bidi-weak, and splitting the
                      resolution across runs mis-orders them under RTL. */}
                  <div dir="auto" className="mt-1 text-xs text-muted-foreground">
                    {t('adm.hold.applied', { rel: formatRelativeTime(hold.applied_at), date: formatDate(hold.applied_at) })}
                    {hold.released_at && t('adm.hold.released_suffix', { rel: formatRelativeTime(hold.released_at) })}
                  </div>
                </div>
                {hold.is_active && (
                  <Button variant="outline" size="sm" onClick={() => setPendingRelease(hold)} disabled={release.isPending}>
                    <ShieldOff className="h-4 w-4" /> {t('adm.hold.release')}
                  </Button>
                )}
              </Card>
            </li>
          ))}
        </ul>
      )}

      <ReleaseHoldDialog
        hold={pendingRelease}
        onClose={() => setPendingRelease(null)}
        onConfirm={(reason, approver) =>
          pendingRelease && release.mutate({ id: pendingRelease.id, reason, approver })
        }
        loading={release.isPending}
      />
    </div>
  )
}

function ReleaseHoldDialog({
  hold,
  onClose,
  onConfirm,
  loading,
}: {
  hold: LegalHold | null
  onClose: () => void
  onConfirm: (reason: string, approver: string) => void
  loading: boolean
}) {
  const { t } = useTranslation('common')
  const [reason, setReason] = useState('')
  const [approver, setApprover] = useState('')
  return (
    <Dialog
      open={!!hold}
      onOpenChange={(o) => { if (!o) { onClose(); setReason(''); setApprover('') } }}
      title={hold ? t('adm.hold.release_named', { name: hold.name }) : t('adm.hold.release_title')}
      description={t('adm.hold.release_desc')}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (!reason.trim()) { toast.error(t('adm.hold.err_reason')); return }
          if (!approver.trim()) { toast.error(t('adm.hold.err_approver')); return }
          if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(approver.trim())) {
            toast.error(t('adm.hold.err_approver_uuid'))
            return
          }
          onConfirm(reason.trim(), approver.trim())
        }}
        className="space-y-4"
      >
        <Textarea
          label={t('adm.hold.reason_label')}
          placeholder={t('adm.hold.reason_ph')}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          required
          rows={3}
        />
        <Input
          label={t('adm.hold.approver_label')}
          placeholder={t('adm.hold.approver_ph')}
          value={approver}
          onChange={(e) => setApprover(e.target.value)}
          required
        />
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="ghost" onClick={onClose} disabled={loading}>{t('adm.hold.cancel')}</Button>
          <Button type="submit" variant="destructive" loading={loading} disabled={loading}>
            {t('adm.hold.release_confirm')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

function SegmentedFilter({ value, onChange }: { value: StatusFilter; onChange: (v: StatusFilter) => void }) {
  const { t } = useTranslation('common')
  const opts: { value: StatusFilter; label: string }[] = [
    { value: 'active', label: t('adm.hold.f_active') },
    { value: 'released', label: t('adm.hold.f_released') },
    { value: 'all', label: t('adm.hold.f_all') },
  ]
  return (
    <div className="inline-flex gap-1 rounded-md bg-muted/60 p-1 text-xs" role="tablist">
      {opts.map((o) => {
        const active = value === o.value
        return (
          <button
            key={o.value}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(o.value)}
            className={cn(
              'rounded-sm px-3 py-1.5 transition-all',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              active ? 'bg-background text-foreground shadow-neu-sm font-medium' : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

function extractMessage(err: unknown): string {
  if (typeof err === 'object' && err && 'message' in err) {
    const m = (err as { message?: string }).message
    if (m) return m
  }
  return i18n.t('adm.hold.err_release', { ns: 'common', defaultValue: 'Release failed' })
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/legal?tab=holds). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/legal-holds')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/legal', search: { tab: 'holds' }, replace: true })
  },
})
