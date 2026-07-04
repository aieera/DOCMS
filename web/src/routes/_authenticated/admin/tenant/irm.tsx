import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, Lock, ShieldCheck } from 'lucide-react'
import { toast } from 'sonner'

import { useAppMutation } from '@/hooks/useAppMutation'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { Card } from '@/components/ui/card'
import { Spinner } from '@/components/ui/Spinner'
import {
  listIrmContainers,
  listContainerLicenses,
  revokeLicense,
  type IrmContainerLicense,
  type IrmContainerSummary,
} from '@/api/irm'

// /admin/tenant/irm — Protected exports dashboard. Read-only list of
// IRM containers and their per-recipient licenses, plus a per-license
// revoke. Revoking blocks the next open of that recipient's link.

export const Route = createFileRoute('/_authenticated/admin/tenant/irm')({
  component: IrmPage,
})

function fmt(ts: string | null | undefined): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString()
}

function IrmPage() {
  const containersQ = useQuery({
    queryKey: ['admin', 'irm', 'containers'],
    queryFn: listIrmContainers,
  })
  const [expanded, setExpanded] = useState<string | null>(null)

  return (
    <div className="mx-auto max-w-4xl p-6">
      <PageHeader
        title="Protected exports"
        description="IRM licenses issued by Protect & share — recipients, expiry, opens, and revocation."
      />

      <ExplainerBanner />

      {containersQ.isLoading ? (
        <Spinner />
      ) : (containersQ.data ?? []).length === 0 ? (
        <Card className="p-6 text-sm text-muted-foreground">
          No protected containers yet. Use “Protect &amp; share” on a document to seal it and
          issue per-recipient licenses.
        </Card>
      ) : (
        <div className="space-y-3">
          {(containersQ.data ?? []).map((c) => (
            <ContainerRow
              key={c.id}
              container={c}
              expanded={expanded === c.id}
              onToggle={() => setExpanded((cur) => (cur === c.id ? null : c.id))}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function ExplainerBanner() {
  return (
    <section className="mb-6 rounded-lg border border-blue-500/40 bg-blue-50/60 p-4 text-sm dark:bg-blue-950/20">
      <div className="flex items-start gap-3">
        <ShieldCheck className="mt-0.5 h-5 w-5 shrink-0 text-blue-600" />
        <div className="text-muted-foreground">
          <p className="font-semibold text-foreground">How it works</p>
          <p className="mt-1">
            Each protected container holds an encrypted copy of a document and a set of
            per-recipient licenses. Opening a link makes an online license check that logs the
            open and enforces expiry. Revoking a license blocks that recipient's next open.
            This is access control and audit — not screenshot prevention.
          </p>
        </div>
      </div>
    </section>
  )
}

function ContainerRow({
  container,
  expanded,
  onToggle,
}: {
  container: IrmContainerSummary
  expanded: boolean
  onToggle: () => void
}) {
  return (
    <Card className="overflow-hidden">
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-3 p-4 text-left hover:bg-accent/40"
        aria-expanded={expanded}
      >
        {expanded ? (
          <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
        )}
        <Lock className="h-4 w-4 shrink-0 text-primary" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium">{container.document_title}</div>
          <div className="mt-0.5 text-xs text-muted-foreground">
            Created {fmt(container.created_at)} · Expires {fmt(container.expires_at)} ·{' '}
            {container.recipient_count}{' '}
            {container.recipient_count === 1 ? 'recipient' : 'recipients'}
          </div>
        </div>
        {container.revoked && (
          <span className="shrink-0 rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-700 dark:bg-red-950/40 dark:text-red-300">
            Revoked
          </span>
        )}
      </button>

      {expanded && <LicenseList containerId={container.id} />}
    </Card>
  )
}

function LicenseList({ containerId }: { containerId: string }) {
  const qc = useQueryClient()
  const licensesQ = useQuery({
    queryKey: ['admin', 'irm', 'containers', containerId, 'licenses'],
    queryFn: () => listContainerLicenses(containerId),
  })
  const [confirm, setConfirm] = useState<IrmContainerLicense | null>(null)

  const revoke = useAppMutation({
    mutationFn: (licenseId: string) => revokeLicense(licenseId),
    onSuccess: () => {
      toast.success('License revoked')
      setConfirm(null)
      qc.invalidateQueries({
        queryKey: ['admin', 'irm', 'containers', containerId, 'licenses'],
      })
      qc.invalidateQueries({ queryKey: ['admin', 'irm', 'containers'] })
    },
    defaultErrorMessage: 'Could not revoke the license',
  })

  if (licensesQ.isLoading) {
    return (
      <div className="border-t border-border p-4">
        <Spinner />
      </div>
    )
  }

  const licenses = licensesQ.data ?? []

  return (
    <div className="border-t border-border">
      {licenses.length === 0 ? (
        <p className="p-4 text-sm text-muted-foreground">No licenses on this container.</p>
      ) : (
        <table className="w-full text-sm">
          <thead className="bg-muted/50 text-left text-xs uppercase text-muted-foreground">
            <tr>
              <th className="px-3 py-2">Recipient</th>
              <th className="px-3 py-2">Expires</th>
              <th className="px-3 py-2">Opens</th>
              <th className="px-3 py-2">Last opened</th>
              <th className="px-3 py-2">Status</th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {licenses.map((lic) => (
              <tr key={lic.license_id}>
                <td className="px-3 py-2">
                  <span className="font-medium">{lic.recipient_ref}</span>
                  <span className="ms-1 text-xs text-muted-foreground">
                    ({lic.recipient_type})
                  </span>
                </td>
                <td className="px-3 py-2">{fmt(lic.expires_at)}</td>
                <td className="px-3 py-2 font-mono">{lic.open_count}</td>
                <td className="px-3 py-2">{fmt(lic.last_opened_at)}</td>
                <td className="px-3 py-2">
                  {lic.revoked_at ? (
                    <span className="rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-700 dark:bg-red-950/40 dark:text-red-300">
                      Revoked
                    </span>
                  ) : (
                    <span className="rounded-full bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300">
                      Active
                    </span>
                  )}
                </td>
                <td className="px-3 py-2 text-right">
                  {!lic.revoked_at && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="gap-1 text-red-600 hover:text-red-700"
                      onClick={() => setConfirm(lic)}
                      disabled={revoke.isPending}
                    >
                      Revoke
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(o) => !o && setConfirm(null)}
        title="Revoke this license?"
        description={
          confirm
            ? `${confirm.recipient_ref} will be blocked from opening this document on their next attempt. This cannot be undone.`
            : ''
        }
        confirmLabel="Revoke"
        destructive
        loading={revoke.isPending}
        onConfirm={() => confirm && revoke.mutate(confirm.license_id)}
      />
    </div>
  )
}
