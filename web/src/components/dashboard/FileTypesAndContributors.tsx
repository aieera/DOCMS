import { useTranslation } from 'react-i18next'

import { BreakdownBars } from './BreakdownBars'
import { useDashboardMetrics } from './useDashboardMetrics'

// Both panels read the same facet query, so they share one hook call
// rather than each mounting their own.
export function FileTypesAndContributors() {
  const { t } = useTranslation('common')
  const { fileTypes, contributors, isLoading, isError, isUnavailable, refetch } = useDashboardMetrics()
  return (
    <div className="grid gap-6 sm:grid-cols-2">
      <BreakdownBars
        title={t('dashboard.widgets.file_types_title')}
        // M7 (spec §2.1.4): say what the share is of.
        subtitle={t('dashboard.widgets.of_indexed')}
        slices={fileTypes}
        isLoading={isLoading}
        isError={isError}
        isUnavailable={isUnavailable}
        onRetry={refetch}
        emptyLabel={t('dashboard.widgets.file_types_empty')}
        unit={t('dashboard.widgets.unit_documents')}
        delayIndex={7}
      />
      <BreakdownBars
        title={t('dashboard.widgets.contributors_title')}
        subtitle={t('dashboard.widgets.of_indexed')}
        slices={contributors}
        isLoading={isLoading}
        isError={isError}
        isUnavailable={isUnavailable}
        onRetry={refetch}
        emptyLabel={t('dashboard.widgets.contributors_empty')}
        unit={t('dashboard.widgets.unit_documents')}
        delayIndex={8}
      />
    </div>
  )
}
