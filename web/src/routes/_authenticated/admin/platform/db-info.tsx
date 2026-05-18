import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Database, CheckCircle2, AlertTriangle, XCircle, Clock } from 'lucide-react'

import { getDBInfo, type Capability, type CapabilityStatus, type DriverInfo } from '@/api/platform'
import { PageHeader } from '@/components/shared/PageHeader'
import { Spinner } from '@/components/ui/Spinner'

// /admin/platform/db-info — driver + version + capability matrix (ADR 0094).
// Today: PostgreSQL 16 only; alt drivers all "not_implemented".
// Future: same UI surfaces the actual state as alt-driver phases ship.

export const Route = createFileRoute('/_authenticated/admin/platform/db-info')({
  component: DBInfoPage,
})

function DBInfoPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ['db-info'],
    queryFn: getDBInfo,
    refetchInterval: 30_000,
  })

  return (
    <div className="mx-auto max-w-5xl p-6">
      <PageHeader
        title="Database driver info"
        description="Active driver + live version + capability matrix vs. the §13.3 alternative-database roadmap. Read-only. See ADR 0094 for the design plan."
      />

      {isLoading && <Spinner />}
      {error && (
        <div className="rounded-lg border border-red-500/40 bg-red-50 p-4 text-sm dark:bg-red-950/30">
          Failed to load DB info: {(error as Error).message}
        </div>
      )}

      {data && (
        <>
          {/* ---- Active driver banner ---- */}
          <section className="mb-6 rounded-lg border border-border bg-card p-4">
            <div className="flex items-start gap-3">
              <Database className="mt-0.5 h-5 w-5 text-emerald-500" />
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-semibold">{data.active.display_name}</span>
                  <span className="inline-flex items-center gap-1 rounded-full bg-emerald-100 px-2 py-0.5 text-xs text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200">
                    Active
                  </span>
                </div>
                {data.active.version && (
                  <p className="mt-1 break-all text-xs text-muted-foreground">
                    <code className="font-mono">{data.active.version}</code>
                  </p>
                )}
                <p className="mt-2 text-xs text-muted-foreground">
                  ADR <a href="/docs/adr/0094-alternative-db-adapters" className="underline">{data.adr}</a> covers the
                  multi-quarter plan to add MySQL / Oracle / SQL Server support. The matrix below
                  surfaces what works on each driver and what doesn't.
                </p>
              </div>
            </div>
          </section>

          {/* ---- Capability matrix ---- */}
          <section>
            <h2 className="mb-3 text-lg font-semibold">Feature matrix</h2>
            <div className="overflow-x-auto rounded-lg border border-border">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border bg-muted/40">
                    <th className="px-3 py-2 text-start font-medium">Capability</th>
                    {[data.active, ...data.alternate_drivers].map((d) => (
                      <th key={d.name} className="px-3 py-2 text-start font-medium">
                        <div className="flex items-center gap-1.5">
                          {d.display_name}
                          {d.status === 'not_implemented' && (
                            <Clock className="h-3 w-3 text-muted-foreground" />
                          )}
                        </div>
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {data.active.capabilities.map((cap) => (
                    <CapabilityRow
                      key={cap.name}
                      cap={cap}
                      drivers={[data.active, ...data.alternate_drivers]}
                    />
                  ))}
                </tbody>
              </table>
            </div>

            <div className="mt-4 flex flex-wrap gap-3 text-xs text-muted-foreground">
              <StatusLegend status="native" />
              <StatusLegend status="degraded" />
              <StatusLegend status="unsupported" />
              <StatusLegend status="not_implemented" />
            </div>
          </section>

          <section className="mt-6 rounded-lg border border-amber-500/30 bg-amber-50/60 p-4 text-sm dark:bg-amber-950/20">
            <strong>Honest status:</strong> only PostgreSQL is implemented today. The columns for
            MySQL, Oracle, and SQL Server reflect the design plan in ADR 0094, not shipped code.
            Each driver phase is multi-month work and ships its own subsequent ADR.
          </section>
        </>
      )}
    </div>
  )
}

function CapabilityRow({ cap, drivers }: { cap: Capability; drivers: DriverInfo[] }) {
  return (
    <tr className="border-b border-border last:border-b-0">
      <td className="px-3 py-2 align-top">
        <div className="font-mono text-xs font-semibold">{cap.name}</div>
        <div className="mt-0.5 text-xs text-muted-foreground">{cap.description}</div>
      </td>
      {drivers.map((d) => {
        const driverCap = d.capabilities.find((c) => c.name === cap.name)
        if (!driverCap) {
          return <td key={d.name} className="px-3 py-2 text-xs text-muted-foreground">—</td>
        }
        return (
          <td key={d.name} className="px-3 py-2 align-top">
            <StatusBadge status={driverCap.status} />
            {driverCap.notes && (
              <p className="mt-1 text-xs text-muted-foreground">{driverCap.notes}</p>
            )}
          </td>
        )
      })}
    </tr>
  )
}

function StatusBadge({ status }: { status: CapabilityStatus }) {
  const config = {
    native: {
      icon: <CheckCircle2 className="h-3 w-3" />,
      cls: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200',
      label: 'Native',
    },
    degraded: {
      icon: <AlertTriangle className="h-3 w-3" />,
      cls: 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200',
      label: 'Degraded',
    },
    unsupported: {
      icon: <XCircle className="h-3 w-3" />,
      cls: 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-200',
      label: 'Unsupported',
    },
    not_implemented: {
      icon: <Clock className="h-3 w-3" />,
      cls: 'bg-muted text-muted-foreground',
      label: 'Not implemented',
    },
  }[status]
  return (
    <span className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs ${config.cls}`}>
      {config.icon}
      {config.label}
    </span>
  )
}

function StatusLegend({ status }: { status: CapabilityStatus }) {
  return (
    <span className="inline-flex items-center gap-1">
      <StatusBadge status={status} />
    </span>
  )
}
