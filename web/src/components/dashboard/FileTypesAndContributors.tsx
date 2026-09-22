import { BreakdownBars } from './BreakdownBars'
import { useDashboardMetrics } from './useDashboardMetrics'

// Both panels read the same facet query, so they share one hook call
// rather than each mounting their own.
export function FileTypesAndContributors() {
  const { fileTypes, contributors, isLoading, isError, isUnavailable, refetch } = useDashboardMetrics()
  return (
    <div className="grid gap-6 sm:grid-cols-2">
      <BreakdownBars
        title="File types"
        // M7 (spec §2.1.4): say what the share is of.
        subtitle="Of indexed documents"
        slices={fileTypes}
        isLoading={isLoading}
        isError={isError}
        isUnavailable={isUnavailable}
        onRetry={refetch}
        emptyLabel="No files indexed"
        unit="documents"
        delayIndex={7}
      />
      <BreakdownBars
        title="Top contributors"
        subtitle="Of indexed documents"
        slices={contributors}
        isLoading={isLoading}
        isError={isError}
        isUnavailable={isUnavailable}
        onRetry={refetch}
        emptyLabel="No contributors yet"
        unit="documents"
        delayIndex={8}
      />
    </div>
  )
}
