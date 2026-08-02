import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { ShieldCheck, Check } from 'lucide-react'

import { getPermissionMatrix, type PermissionCell } from '@/api/permissionsMatrix'
import { PageHeader } from '@/components/shared/PageHeader'
import { Skeleton } from '@/components/ui/Skeleton'
import { Badge } from '@/components/ui/shadcn/badge'

// Capability ordering mirrors the underlying policy hierarchy:
//   admin > delete > edit > share > view
// Cells render an "included" tick for every capability at or below
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

// The policy backend stamps `source` and `notes` with literal rego
// rule numbers / file references that aren't useful to admins and
// expose implementation details. Map known patterns to plain
// language at render time; unknown sources pass through (best the
// UI can do without losing information).
function prettySource(source: string): string {
  if (!source) return ''
  if (/user_role=owner/i.test(source)) return 'Built-in: owner role'
  if (/user_role=admin/i.test(source)) return 'Built-in: admin role'
  if (/cascade/i.test(source))         return 'Inherited from workspace'
  if (/^rego rule \d+$/i.test(source)) return 'Workspace-level grant'
  if (/^rego/i.test(source))           return 'Policy grant'
  return source
}

// Strip parenthetical rego references from notes
// ("(policy.rego rules 3 + 4)", "(rego deny-rule #1)") without
// losing the surrounding sentence.
function prettyNote(note: string): string {
  return note
    .replace(/\s*\((?:policy\.)?rego[^)]*\)/gi, '')
    .replace(/\s+\./g, '.')
    .trim()
}

export function PermissionsPage() {
  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'permissions-matrix'],
    queryFn: getPermissionMatrix,
  })

  if (isLoading) {
    return (
      <div>
        <PageHeader
          title="Permission Matrix"
          description="View the permissions assigned to each role across all resource types. This matrix is read-only and reflects the current access policy."
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
        description="View the permissions assigned to each role across all resource types. This matrix is read-only and reflects the current access policy."
      />

      <div className="overflow-auto rounded-lg border border-border">
        <table className="w-full text-sm">
          <thead className="bg-muted/40">
            <tr>
              <th className="px-4 py-2 text-start">Role</th>
              {(data.resource_types ?? []).map((r) => (
                <th key={r} className="px-4 py-2 text-start">
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
                          — {prettySource(c?.source ?? '') || 'no baseline'}
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
                              {prettySource(c.source)}
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
        <ul className="list-disc ps-6 text-xs text-muted-foreground">
          {(data.notes ?? []).map((n, i) => (
            <li key={i}>{prettyNote(n)}</li>
          ))}
        </ul>
        <div className="pt-2">
          <Badge variant="in_review">Read-only</Badge>
          <span className="ms-2 text-xs text-muted-foreground">
            Changes to the matrix ship with platform releases — they can't be edited from this page.
          </span>
        </div>
      </div>
    </div>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/identity?sub=permissions). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/permissions')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/identity', search: { sub: 'permissions' }, replace: true })
  },
})
