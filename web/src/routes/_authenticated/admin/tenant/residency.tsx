// /admin/tenant/residency — owner-gated tenant data-residency policy
// (Blueprint §9.1). Three sections:
//
//   1. Org-wide allowed_regions multi-select.
//   2. Org default region for new uploads.
//   3. Read-only per-workspace defaults + per-region document counts.

import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'

import { PageHeader } from '@/components/shared/PageHeader'
import { AdminGuard } from '@/features/admin/internalAuth/AdminGuard'
import { Button } from '@/components/ui/Button'
import { RegionPicker } from '@/components/shared/RegionPicker'
import { RegionPinBadge } from '@/components/shared/RegionPinBadge'
import { REGIONS } from '@/lib/regions'
import { useAuthStore } from '@/store/authStore'
import {
  getResidencyStats,
  getTenantResidencyPolicy,
  putTenantResidencyPolicy,
  listWorkspaceRegions,
} from '@/api/residency'

function TenantResidencyPage() {
  const user = useAuthStore((s) => s.user)
  const canEdit = user?.role === 'owner'
  const qc = useQueryClient()

  const policy = useQuery({
    queryKey: ['tenant', 'residency-policy'],
    queryFn: getTenantResidencyPolicy,
  })
  const stats = useQuery({
    queryKey: ['residency', 'stats'],
    queryFn: getResidencyStats,
  })
  const workspaces = useQuery({
    queryKey: ['tenant', 'workspace-regions'],
    queryFn: listWorkspaceRegions,
  })

  const [form, setForm] = useState<{ allowed: string[]; defaultRegion: string }>({
    allowed: [],
    defaultRegion: 'me-south-1',
  })
  useEffect(() => {
    if (policy.data) {
      setForm({ allowed: policy.data.allowed_regions ?? [], defaultRegion: policy.data.default_region_pin })
    }
  }, [policy.data])

  const save = useMutation({
    mutationFn: () => putTenantResidencyPolicy({
      allowed_regions: form.allowed,
      default_region_pin: form.defaultRegion,
    }),
    onSuccess: () => {
      toast.success('Residency policy saved')
      qc.invalidateQueries({ queryKey: ['tenant', 'residency-policy'] })
    },
    onError: () => toast.error('Save failed'),
  })

  const toggleAllowed = (code: string) => {
    setForm((f) => {
      const has = f.allowed.includes(code)
      return { ...f, allowed: has ? f.allowed.filter((c) => c !== code) : [...f.allowed, code] }
    })
  }

  return (
    <AdminGuard>
      <div className="space-y-8">
        <PageHeader
          title="Tenant residency policy"
          description="Allowed regions, default region for new uploads, and per-workspace overrides. Changes apply to new documents only — existing documents keep their current pin."
        />

        <form
          data-testid="residency-policy-form"
          onSubmit={(e) => { e.preventDefault(); save.mutate() }}
          className="max-w-3xl space-y-8"
        >
          <section>
            <h2 className="text-base font-semibold">Allowed regions</h2>
            <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
              When set, document creation is rejected if the chosen region isn't on this list. Leave empty to allow any supported region.
            </p>
            <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-3">
              {REGIONS.map((r) => (
                <label
                  key={r.code}
                  data-testid={`allowed-region-${r.code}`}
                  className="flex items-start gap-2 rounded border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2 py-2 text-sm"
                >
                  <input
                    type="checkbox"
                    checked={form.allowed.includes(r.code)}
                    disabled={!canEdit}
                    onChange={() => toggleAllowed(r.code)}
                    className="mt-0.5"
                  />
                  <span className="min-w-0">
                    <span className="block font-mono text-xs">{r.code}</span>
                    <span className="block text-xs text-[var(--color-text-secondary)]">{r.displayName} · {r.boundary}</span>
                  </span>
                </label>
              ))}
            </div>
          </section>

          <section>
            <h2 className="text-base font-semibold">Default region for new tenants</h2>
            <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
              Used when neither the request nor the workspace specifies a region. Existing documents are not affected.
            </p>
            <div className="mt-2 max-w-sm">
              <RegionPicker
                value={form.defaultRegion}
                onChange={(v) => setForm((f) => ({ ...f, defaultRegion: v }))}
                allowedRegions={form.allowed}
                disabled={!canEdit}
                testId="default-region-picker"
              />
            </div>
          </section>

          <div className="flex items-center gap-3">
            <Button type="submit" disabled={!canEdit} loading={save.isPending} data-testid="save-residency-policy">
              Save residency policy
            </Button>
            {!canEdit && (
              <span className="text-xs text-[var(--color-text-secondary)]">
                Only the tenant owner can change these values.
              </span>
            )}
          </div>
        </form>

        <section className="max-w-3xl">
          <h2 className="text-base font-semibold">Per-workspace defaults</h2>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
            Each workspace can override the org default. Editing a workspace's region pin is done from its settings page.
          </p>
          {workspaces.isLoading && <p className="mt-2 text-sm text-[var(--color-text-secondary)]">Loading workspaces…</p>}
          {workspaces.data && workspaces.data.length > 0 && (
            <ul className="mt-3 divide-y divide-[var(--color-border)] rounded border border-[var(--color-border)]">
              {workspaces.data.map((w) => (
                <li
                  key={w.workspace_id}
                  data-testid={`workspace-region-${w.workspace_id}`}
                  className="flex items-center justify-between px-3 py-2 text-sm"
                >
                  <span className="font-medium">{w.workspace_name}</span>
                  <RegionPinBadge region={w.region_pin} size="sm" />
                </li>
              ))}
            </ul>
          )}
        </section>

        <section className="max-w-3xl">
          <h2 className="text-base font-semibold">Documents per region</h2>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
            Read-only. The reconciliation worker re-computes these counts daily.
          </p>
          {stats.isLoading && <p className="mt-2 text-sm text-[var(--color-text-secondary)]">Loading stats…</p>}
          {stats.data && stats.data.length > 0 && (
            <table className="mt-3 w-full text-sm" data-testid="residency-stats-table">
              <thead className="border-b border-[var(--color-border)] text-left text-xs uppercase text-[var(--color-text-secondary)]">
                <tr>
                  <th className="py-2">Region</th>
                  <th className="py-2 text-end tabular-nums">Documents</th>
                  <th className="py-2 text-end tabular-nums">Off-region</th>
                </tr>
              </thead>
              <tbody>
                {stats.data.map((r) => (
                  <tr key={r.region} className="border-b border-[var(--color-border)]/60">
                    <td className="py-2"><RegionPinBadge region={r.region} size="sm" /></td>
                    <td className="py-2 text-end tabular-nums">{r.doc_count.toLocaleString()}</td>
                    <td className="py-2 text-end tabular-nums">
                      {(r.off_region_count ?? 0) > 0 ? (
                        <span className="font-medium text-red-700 dark:text-red-300">
                          {r.off_region_count?.toLocaleString()}
                        </span>
                      ) : (
                        <span className="text-emerald-700 dark:text-emerald-300">0</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      </div>
    </AdminGuard>
  )
}

export const Route = createFileRoute('/_authenticated/admin/tenant/residency')({
  component: TenantResidencyPage,
})
