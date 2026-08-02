import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { PageHeader } from '@/components/shared/PageHeader'
import { ComplianceDashboard } from '@/components/admin/ComplianceDashboard'
import { Spinner } from '@/components/ui/Spinner'
import { getComplianceOverview } from '@/api/compliance'

export function CompliancePage() {
  // Previously this page hard-coded encryptionCoverage=94 and a
  // storageByRegion list that included a fictional 67 GB in
  // me-south-1. Both numbers contradicted the real DB (56.8%
  // coverage, zero bytes in me-south-1) and were the basis of two
  // alarming dashboard signals. The /admin/compliance/overview
  // endpoint computes them live from documents + content_blobs.
  const { data, isLoading, isError } = useQuery({
    queryKey: ['admin-compliance-overview'],
    queryFn: getComplianceOverview,
    staleTime: 60_000,
  })

  return (
    <div>
      <PageHeader title="Compliance" description="Data residency, encryption, and retention overview" />
      {isLoading ? (
        <div className="flex justify-center py-12"><Spinner className="h-6 w-6" /></div>
      ) : isError ? (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 p-4 text-sm">
          Could not load compliance overview — check the document service is reachable.
        </div>
      ) : (
        <ComplianceDashboard
          docsByState={data?.docs_by_state ?? []}
          storageByRegion={data?.storage_by_region ?? []}
          encryptionCoverage={data?.encryption_coverage ?? 0}
        />
      )}
    </div>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/data-governance?tab=compliance). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/compliance')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/data-governance', search: { tab: 'compliance' }, replace: true })
  },
})
