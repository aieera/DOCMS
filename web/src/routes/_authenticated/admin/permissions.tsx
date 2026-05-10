import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { ShieldCheck, Check } from 'lucide-react'

import { getPermissionMatrix, type PermissionCell } from '@/api/permissionsMatrix'
import { PageHeader } from '@/components/shared/PageHeader'
import { Skeleton } from '@/components/ui/Skeleton'
import { Badge } from '@/components/ui/shadcn/badge'

// Capability ordering mirrors the rego hierarchy:
//   admin > delete > edit > share > view
// Cells render an"included" tick for every capability at or below
// the cell's max_capability so admins see the full inherited set,
// not just the single highest tier.
const CAP_ORDER = ['admin', 'delete', 'edit', 'share', 'view'] as const

function includedCaps(max: string): string[] {
  if (!max) return []
  const idx = CAP_ORDER.indexOf(max as (typeof CAP_ORDER)[number])
  if (idx === -1) return []
  return CAP_ORDER.slice(idx)
}

function cellFor(cells: PermissionCell[], role: string, resource: string): PermissionCell | undefined {
  return cells.find((c) => c.role === role && c.resource_type === resource)
}

function PermissionsPage() {
  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'permissions-matrix'],
    queryFn: getPermissionMatrix,
  })

  if (isLoading) {
    return (
      <div>
        <PageHeader
          title="Permission Matrix"
          description="Read-only view of role → resource → capability from the OPA policy bundle."
        />
        <Skeleton className="h-64" />
      </div>
    )
  }

  if (!data) return null

  return (
    <div>
      <PageHeader
        title="Permission Matrix"
        description="Read-only projection of services/policy/internal/opa/policy.rego — what each role can do on each resource type."
      />

      <div className="overflow-auto rounded-lg border border-border">
        <table className="w-full text-sm">
          <thead className="bg-muted/40">
            <tr>
              <th className="px-4 py-2 text-left">Role</th>
              {(data.resource_types ?? []).map((r) => (
                <th key={r} className="px-4 py-2 text-left">
                  {r}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {(data.roles ?? []).map((role) => (
              <tr key={role} className="border-t border-border">
                <td className="px-4 py-2 font-medium">
                  <div className="flex items-center gap-2">
                    <ShieldCheck className="h-4 w-4" />
                    {role}
                  </div>
                </td>
                {(data.resource_types ?? []).map((rt) => {
                  const c = cellFor(data.cells, role, rt)
                  const caps = includedCaps(c?.max_capability ?? '')
                  return (
                    <td key={rt} className="px-4 py-2 align-top">
                      {caps.length === 0 ? (
                        <span className="text-xs text-muted-foreground">
                          — {c?.source ?? 'no baseline'}
                        </span>
                      ) : (
                        <>
                          <div className="flex flex-wrap gap-1">
                            {CAP_ORDER.map((cap) => (
                              <span
                                key={cap}
                                className={`inline-flex items-center gap-0.5 rounded-full px-2 py-0.5 text-xs ${
                                  caps.includes(cap)
                                    ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200'
                                    : 'bg-muted text-muted-foreground line-through '
                                }`}
                              >
                                {caps.includes(cap) && <Check className="h-3 w-3" />}
                                {cap}
                              </span>
                            ))}
                          </div>
                          {c?.source && (
                            <div className="mt-1 text-xs text-muted-foreground">
                              {c.source}
                            </div>
                          )}
                        </>
                      )}
                    </td>
                  )
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="mt-6 space-y-2 rounded-lg border border-border bg-card p-4 text-sm">
        <div className="flex items-center gap-2 font-medium">
          <ShieldCheck className="h-4 w-4" />
          Notes
        </div>
        <ul className="list-disc pl-6 text-xs text-muted-foreground">
          {(data.notes ?? []).map((n, i) => (
            <li key={i}>{n}</li>
          ))}
        </ul>
        <div className="pt-2">
          <Badge variant="in_review">Read-only</Badge>
          <span className="ml-2 text-xs text-muted-foreground">
            Changes to the matrix ship via a new policy.rego release + matching matrix update in the
            policy service.
          </span>
        </div>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/permissions')({ component: PermissionsPage })
