import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, PieChart, Pie, Cell, Legend } from 'recharts'

import { formatFileSize, lifecycleStateLabel } from '@/lib/formatters'

const COLORS = ['#1E40AF', '#10B981', '#F59E0B', '#EF4444', '#8B5CF6', '#6B7280']

// Consistent storage label. Recharts inline pie labels can clip on
// narrow slices, so this string is also rendered through the legend
// below the chart — the legend never clips and is the authoritative
// source for the per-region value. formatFileSize carries the localised
// unit symbol and the LRI…PDI isolate; the API reports whole GB.
const formatStorage = (gb: number): string => formatFileSize(gb * 1024 ** 3)

interface Props {
  docsByState?: { name: string; count: number }[]
  storageByRegion?: { name: string; gb: number }[]
  encryptionCoverage?: number
}

export function ComplianceDashboard({ docsByState = [], storageByRegion = [], encryptionCoverage = 0 }: Props) {
  const { t, i18n } = useTranslation('common')
  // The API sends raw lifecycle states (draft, active, superseded…) and the
  // bar axis rendered them as-is. `i18n.language` belongs in the deps: these
  // are TRANSLATED labels and the locale arrives with /auth/me, after the
  // first render. Region names stay as-is — they are AWS region codes.
  const stateData = useMemo(
    () => docsByState.map((d) => ({ ...d, name: lifecycleStateLabel(d.name) })),
    [docsByState, i18n.language],
  )
  return (
    <div className="grid grid-cols-1 gap-6 sm:grid-cols-2">
      <div className="rounded-lg bg-card p-4 shadow-neu">
        <h3 className="mb-3 text-sm font-semibold">{t('adm.comp.docs_by_state')}</h3>
        <ResponsiveContainer width="100%" height={220}>
          <BarChart data={stateData}>
            <XAxis dataKey="name" tick={{ fontSize: 11 }} />
            {/* Baseline at 0: with a single state (e.g. only "draft") recharts'
                auto Y-domain collapses to [count, count], so the bar gets 0
                height and the chart looks empty. allowDecimals=false keeps the
                integer-count ticks clean. */}
            <YAxis tick={{ fontSize: 11 }} domain={[0, 'auto']} allowDecimals={false} />
            <Tooltip />
            <Bar dataKey="count" fill="#1E40AF" radius={[4, 4, 0, 0]} minPointSize={2} />
          </BarChart>
        </ResponsiveContainer>
      </div>
      <div className="rounded-lg bg-card p-4 shadow-neu">
        <h3 className="mb-3 text-sm font-semibold">{t('adm.comp.storage_by_region')}</h3>
        <ResponsiveContainer width="100%" height={260}>
          <PieChart>
            <Pie
              data={storageByRegion}
              dataKey="gb"
              nameKey="name"
              cx="50%"
              cy="45%"
              outerRadius={70}
              label={({ name, gb }) => `${name}: ${formatStorage(gb)}`}
            >
              {storageByRegion.map((_, i) => <Cell key={i} fill={COLORS[i % COLORS.length]} />)}
            </Pie>
            <Tooltip formatter={(value: number) => formatStorage(value)} />
            <Legend
              verticalAlign="bottom"
              height={36}
              formatter={(value: string, entry) => {
                const gb = (entry?.payload as { gb?: number } | undefined)?.gb
                return t('adm.comp.legend_region', { region: value, size: gb != null ? formatStorage(gb) : '—' })
              }}
            />
          </PieChart>
        </ResponsiveContainer>
      </div>
      <div className="col-span-2 rounded-lg bg-card p-4 shadow-neu">
        <h3 className="mb-3 text-sm font-semibold">{t('adm.comp.encryption_coverage')}</h3>
        <div className="flex items-center gap-4">
          <div className="h-3 flex-1 overflow-hidden rounded-full bg-muted">
            <div className="h-full rounded-full bg-success transition-all" style={{ width: `${encryptionCoverage}%` }} />
          </div>
          <span className="text-sm font-medium">{encryptionCoverage}%</span>
        </div>
      </div>
    </div>
  )
}
