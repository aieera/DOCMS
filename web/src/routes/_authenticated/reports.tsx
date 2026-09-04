// Report builder (ADR 0119): pick dataset → dimensions/measures/filters,
// run against the governed query API, chart + table, save + schedule,
// export CSV / print-to-PDF.
//
// Chart styling follows the dataviz method: thin marks (≤24px bars,
// 4px rounded data-ends, 2px lines), hairline solid grid, recessive
// axes, a validated categorical palette (adjacent-pair CVD ΔE ≥ 8 on
// the white card surface), single-series marks in the brand hue, no
// legend for one series, donut capped at 6 segments with the tail
// folded into "Other", and the table view always present as the
// accessibility twin.
import { createFileRoute } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import {
  BarChart3, Play, Save, Trash2, Download, Printer, CalendarClock, Plus, X,
  Table2, LineChart as LineChartIcon, PieChart as PieChartIcon, Layers, Hash, Crown,
} from 'lucide-react'
import {
  BarChart, Bar, LineChart, Line, PieChart, Pie, Cell, CartesianGrid,
  XAxis, YAxis, Tooltip as ChartTooltip, ResponsiveContainer, Legend,
} from 'recharts'

import {
  listDatasets, runQuery, listReports, createReport, updateReport, deleteReport, toCSV,
  type AnalyticsQuery, type QueryResult, type SavedReport, type SaveReportInput,
} from '@/api/analytics'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/shadcn/input'
import { formatFileSize } from '@/lib/formatters'
import { dimensionLabel, formatCell } from '@/lib/reportFormat'
import { cn } from '@/lib/cn'

export const Route = createFileRoute('/_authenticated/reports')({
  component: ReportsPage,
})

type ChartType = 'table' | 'bar' | 'line' | 'pie'

// Validated categorical palette (dataviz reference order — adjacent-pair
// CVD ΔE ≥ 9.1, normal-vision ≥ 19.6 on #ffffff; three light slots are
// sub-3:1 which is why the table view always renders below the chart).
const CHART_COLORS = ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#4a3aa7', '#e34948']
// Single-series marks wear the brand hue (--primary), not a slot color.
const SERIES = '#286c4c'
// Chart chrome — recessive by design.
const INK_MUTED = '#898781'
const GRID = '#e1e0d9'
const AXIS = '#c3c2b7'

const CHART_TYPES: Array<{ type: ChartType; label: string; icon: typeof Table2 }> = [
  { type: 'table', label: 'Table', icon: Table2 },
  { type: 'bar', label: 'Bar', icon: BarChart3 },
  { type: 'line', label: 'Line', icon: LineChartIcon },
  { type: 'pie', label: 'Donut', icon: PieChartIcon },
]

// Quick-start presets shown on the empty state — one click to a first
// chart, which doubles as a worked example of the builder.
const PRESETS: Array<{ label: string; query: AnalyticsQuery; chart: ChartType }> = [
  {
    label: 'Documents by class',
    chart: 'bar',
    query: { dataset: 'documents', dimensions: ['document_class'], measures: ['count'] },
  },
  {
    label: 'Uploads per month',
    chart: 'line',
    query: { dataset: 'documents', dimensions: ['created_month'], measures: ['count'] },
  },
  {
    label: 'Storage by type',
    chart: 'pie',
    query: { dataset: 'documents', dimensions: ['doc_type'], measures: ['total_size_bytes'] },
  },
]

const isBytesMeasure = (name: string) => /bytes|size/i.test(name)

const formatMeasure = (name: string, v: number): string =>
  isBytesMeasure(name) ? formatFileSize(v) : v.toLocaleString('en-US')

// Compact axis figures: 1.2K / 3.4M — bytes get the file-size treatment.
const formatAxis = (name: string, v: number): string => {
  if (isBytesMeasure(name)) return formatFileSize(v)
  if (Math.abs(v) >= 1_000_000) return `${(v / 1_000_000).toLocaleString('en-US', { maximumFractionDigits: 1 })}M`
  if (Math.abs(v) >= 1_000) return `${(v / 1_000).toLocaleString('en-US', { maximumFractionDigits: 1 })}K`
  return v.toLocaleString('en-US')
}

const humanize = (col: string) =>
  (col.charAt(0).toUpperCase() + col.slice(1)).replace(/_/g, ' ')

function ReportsPage() {
  const qc = useQueryClient()
  const { data: datasets } = useQuery({ queryKey: ['analytics-datasets'], queryFn: listDatasets })
  const { data: reports } = useQuery({ queryKey: ['analytics-reports'], queryFn: listReports })

  // ---- builder state ------------------------------------------------
  const [dataset, setDataset] = useState('documents')
  const [dimensions, setDimensions] = useState<string[]>(['document_class'])
  const [measures, setMeasures] = useState<string[]>(['count'])
  const [filters, setFilters] = useState<Array<{ field: string; values: string }>>([])
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [chartType, setChartType] = useState<ChartType>('bar')
  const [result, setResult] = useState<QueryResult | null>(null)
  // Dimension count SNAPSHOTTED at run time — the chart must not read
  // the live builder state, or toggling a chip after Run corrupts the
  // rendering against the stale result (review finding).
  const [resultDimCount, setResultDimCount] = useState(0)
  const [saveOpen, setSaveOpen] = useState(false)
  // Set when the builder was loaded from a saved report (Save = update).
  const [loadedReport, setLoadedReport] = useState<SavedReport | null>(null)

  const ds = (datasets ?? []).find((d) => d.name === dataset)

  const buildQuery = (): AnalyticsQuery => {
    const f: Record<string, string[]> = {}
    for (const { field, values } of filters) {
      const vals = values.split(',').map((v) => v.trim()).filter(Boolean)
      if (field && vals.length) f[field] = vals
    }
    const tr: { from?: string; to?: string } = {}
    if (from) tr.from = new Date(from).toISOString()
    if (to) tr.to = new Date(to).toISOString()
    return {
      dataset,
      dimensions,
      measures,
      filters: Object.keys(f).length ? f : undefined,
      time_range: tr.from || tr.to ? tr : undefined,
    }
  }

  // Run accepts an explicit query so saved reports and presets execute
  // exactly what was clicked, not whatever the async state happens to be.
  const run = useAppMutation({
    mutationFn: async (q?: AnalyticsQuery) => {
      const query = q ?? buildQuery()
      const res = await runQuery(query)
      return { res, dims: query.dimensions.length }
    },
    onSuccess: ({ res, dims }) => {
      setResult(res)
      setResultDimCount(dims)
    },
    defaultErrorMessage: 'Query failed',
  })

  const del = useAppMutation({
    mutationFn: (id: string) => deleteReport(id),
    onSuccess: () => {
      toast.success('Report deleted')
      void qc.invalidateQueries({ queryKey: ['analytics-reports'] })
    },
    defaultErrorMessage: 'Could not delete report',
  })

  const applyQueryToBuilder = (q: AnalyticsQuery, chart: ChartType) => {
    setDataset(q.dataset)
    setDimensions(q.dimensions ?? [])
    setMeasures(q.measures ?? [])
    setFilters(Object.entries(q.filters ?? {}).map(([field, vals]) => ({ field, values: vals.join(', ') })))
    setFrom(q.time_range?.from ? q.time_range.from.slice(0, 10) : '')
    setTo(q.time_range?.to ? q.time_range.to.slice(0, 10) : '')
    setChartType(chart)
  }

  // Loading a saved report also RUNS it — a report you have to re-run
  // by hand after clicking it isn't "saved", it's a form preset.
  const loadReport = (r: SavedReport) => {
    setLoadedReport(r)
    applyQueryToBuilder(r.query, r.chart_type)
    run.mutate(r.query)
  }

  const runPreset = (p: (typeof PRESETS)[number]) => {
    setLoadedReport(null)
    applyQueryToBuilder(p.query, p.chart)
    run.mutate(p.query)
  }

  const toggle = (list: string[], setList: (v: string[]) => void, name: string, max?: number) => {
    if (list.includes(name)) setList(list.filter((x) => x !== name))
    else if (!max || list.length < max) setList([...list, name])
    else toast.error(`At most ${max}`)
  }

  const exportCSV = () => {
    if (!result) return
    const blob = new Blob([toCSV(result)], { type: 'text/csv;charset=utf-8' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${loadedReport?.name ?? 'report'}.csv`
    a.click()
    URL.revokeObjectURL(a.href)
  }

  const measureName = result?.columns[resultDimCount] ?? measures[0] ?? 'count'
  const chartTitle = result
    ? `${humanize(measureName)} by ${(result.columns.slice(0, resultDimCount) as string[]).map(humanize).join(' · ') || 'total'}`
    : ''

  return (
    <div className="p-6">
      <PageHeader
        title="Reports"
        description="Governed analytics over your documents, versions and tasks."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={exportCSV} disabled={!result} data-testid="export-csv">
              <Download className="h-3.5 w-3.5" /> CSV
            </Button>
            <Button variant="outline" size="sm" onClick={() => window.print()} disabled={!result}>
              <Printer className="h-3.5 w-3.5" /> PDF
            </Button>
            <Button size="sm" onClick={() => setSaveOpen(true)} disabled={measures.length === 0} data-testid="save-report">
              <Save className="h-3.5 w-3.5" /> {loadedReport ? 'Save changes' : 'Save'}
            </Button>
          </>
        }
      />

      <div className="grid gap-5 lg:grid-cols-[300px_minmax(0,1fr)]">
        {/* ---- left rail: saved reports + builder ------------------- */}
        <div className="space-y-4">
          <Card className="p-4">
            <div className="mb-2 flex items-center justify-between">
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Saved reports
              </p>
              {(reports ?? []).length > 0 && (
                <span className="rounded-full bg-muted px-1.5 text-xs text-muted-foreground">
                  {(reports ?? []).length}
                </span>
              )}
            </div>
            {(reports ?? []).length === 0 && (
              <p className="text-xs text-muted-foreground">None yet — build a query and save it.</p>
            )}
            <div className="max-h-56 space-y-0.5 overflow-y-auto">
              {(reports ?? []).map((r) => {
                const Icon = CHART_TYPES.find((c) => c.type === r.chart_type)?.icon ?? BarChart3
                return (
                  <div
                    key={r.id}
                    className={cn(
                      'group flex items-center gap-2 rounded-md px-2 py-1.5',
                      loadedReport?.id === r.id ? 'bg-primary/10' : 'hover:bg-muted',
                    )}
                  >
                    <button
                      type="button"
                      onClick={() => loadReport(r)}
                      className="flex min-w-0 flex-1 items-center gap-2 text-start"
                      data-testid={`report-item-${r.id}`}
                      title={r.description || r.name}
                    >
                      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      <span className={cn('truncate text-sm', loadedReport?.id === r.id && 'font-medium')}>
                        {r.name}
                      </span>
                    </button>
                    {r.schedule_enabled && (
                      <CalendarClock className="h-3.5 w-3.5 shrink-0 text-primary" aria-label="Scheduled" />
                    )}
                    <button
                      type="button"
                      aria-label={`Delete report ${r.name}`}
                      className="shrink-0 rounded p-0.5 text-muted-foreground opacity-0 transition-opacity hover:text-destructive focus-visible:opacity-100 group-hover:opacity-100"
                      onClick={() => { if (confirm(`Delete report "${r.name}"?`)) del.mutate(r.id) }}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  </div>
                )
              })}
            </div>
          </Card>

          <Card className="overflow-hidden">
            <p className="border-b border-border px-4 py-3 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Query builder
            </p>
            <div className="divide-y divide-border">
              <BuilderStep n={1} title="Dataset">
                <select
                  aria-label="Dataset"
                  value={dataset}
                  onChange={(e) => {
                    setDataset(e.target.value)
                    setDimensions([])
                    setMeasures(['count'])
                    setFilters([])
                    setResult(null)
                    setLoadedReport(null)
                  }}
                  className="h-9 w-full rounded-md border border-input bg-muted px-2 text-sm capitalize shadow-neu-inset focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  data-testid="dataset-select"
                >
                  {(datasets ?? []).map((d) => <option key={d.name} value={d.name}>{d.name}</option>)}
                </select>
              </BuilderStep>

              <BuilderStep n={2} title="Group by" hint="max 3">
                <div className="flex flex-wrap gap-1.5">
                  {(ds?.dimensions ?? []).map((d) => (
                    <Chip key={d} selected={dimensions.includes(d)} onClick={() => toggle(dimensions, setDimensions, d, 3)} testid={`dim-${d}`}>
                      {humanize(d)}
                    </Chip>
                  ))}
                </div>
              </BuilderStep>

              <BuilderStep n={3} title="Measure">
                <div className="flex flex-wrap gap-1.5">
                  {(ds?.measures ?? []).map((m) => (
                    <Chip key={m} selected={measures.includes(m)} onClick={() => toggle(measures, setMeasures, m)} testid={`measure-${m}`}>
                      {humanize(m)}
                    </Chip>
                  ))}
                </div>
              </BuilderStep>

              <BuilderStep
                n={4}
                title="Refine"
                hint="optional"
                action={
                  <Button size="sm" variant="ghost" className="h-6 px-1.5 text-xs" onClick={() => setFilters((f) => [...f, { field: ds?.dimensions[0] ?? '', values: '' }])}>
                    <Plus className="h-3 w-3" /> Filter
                  </Button>
                }
              >
                {filters.map((f, i) => (
                  <div key={i} className="mb-1.5 flex items-center gap-1">
                    <select
                      aria-label="Filter field"
                      value={f.field}
                      onChange={(e) => setFilters((fs) => fs.map((x, xi) => (xi === i ? { ...x, field: e.target.value } : x)))}
                      className="h-7 w-28 rounded border border-input bg-muted px-1 text-xs shadow-neu-inset"
                    >
                      {(ds?.dimensions ?? []).map((d) => <option key={d} value={d}>{humanize(d)}</option>)}
                    </select>
                    <Input
                      value={f.values}
                      placeholder="values, comma-sep"
                      onChange={(e) => setFilters((fs) => fs.map((x, xi) => (xi === i ? { ...x, values: e.target.value } : x)))}
                      className="h-7 flex-1 text-xs"
                    />
                    <button type="button" aria-label="Remove filter" onClick={() => setFilters((fs) => fs.filter((_, xi) => xi !== i))}>
                      <X className="h-3.5 w-3.5 text-destructive" />
                    </button>
                  </div>
                ))}
                {/* min-w-0 lets the native date inputs shrink inside the
                    narrow builder column — without it their intrinsic
                    min-width pushes the To field past the card edge. */}
                <div className="flex gap-2">
                  <label className="min-w-0 flex-1 text-xs text-muted-foreground">
                    From
                    <Input type="date" value={from} onChange={(e) => setFrom(e.target.value)} className="mt-0.5 h-8 min-w-0" />
                  </label>
                  <label className="min-w-0 flex-1 text-xs text-muted-foreground">
                    To
                    <Input type="date" value={to} onChange={(e) => setTo(e.target.value)} className="mt-0.5 h-8 min-w-0" />
                  </label>
                </div>
              </BuilderStep>
            </div>

            <div className="border-t border-border p-3">
              <Button
                className="w-full"
                onClick={() => run.mutate(undefined)}
                disabled={run.isPending || measures.length === 0}
                loading={run.isPending}
                data-testid="run-query"
              >
                {!run.isPending && <Play className="h-4 w-4" />} Run query
              </Button>
            </div>
          </Card>
        </div>

        {/* ---- results ---------------------------------------------- */}
        <div className="min-w-0 space-y-4">
          {!result ? (
            <Card className="flex flex-col items-center justify-center gap-4 px-6 py-16 text-center">
              <span className="flex h-14 w-14 items-center justify-center rounded-full bg-primary/10 text-primary">
                <BarChart3 className="h-7 w-7" />
              </span>
              <div>
                <h2 className="text-base font-semibold">Build your first report</h2>
                <p className="mx-auto mt-1 max-w-md text-sm text-muted-foreground">
                  Pick what to group by and what to measure, then run the query — or start from a template:
                </p>
              </div>
              <div className="flex flex-wrap justify-center gap-2">
                {PRESETS.map((p) => (
                  <Button key={p.label} size="sm" variant="outline" onClick={() => runPreset(p)}>
                    {p.label}
                  </Button>
                ))}
              </div>
            </Card>
          ) : (
            /* Hold the previous render at reduced opacity while a re-run
               is in flight — no skeleton flash, no layout jump. */
            <div className={cn('space-y-4 transition-opacity', run.isPending && 'opacity-60')}>
              <SummaryTiles result={result} dimCount={resultDimCount} measureName={measureName} />

              <Card className="overflow-hidden">
                <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-3">
                  <h2 className="min-w-0 truncate text-sm font-semibold" title={chartTitle}>{chartTitle}</h2>
                  <div className="flex rounded-md bg-muted p-0.5 shadow-neu-inset" role="group" aria-label="Chart type">
                    {CHART_TYPES.map(({ type, label, icon: Icon }) => (
                      <button
                        key={type}
                        type="button"
                        onClick={() => setChartType(type)}
                        aria-pressed={chartType === type}
                        className={cn(
                          'flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium transition-colors',
                          chartType === type
                            ? 'bg-primary text-primary-foreground'
                            : 'text-muted-foreground hover:text-foreground',
                        )}
                        data-testid={`chart-${type}`}
                      >
                        <Icon className="h-3.5 w-3.5" />
                        {label}
                      </button>
                    ))}
                  </div>
                </div>
                {chartType !== 'table' && resultDimCount >= 1 && (
                  <div className="h-80 px-4 pb-2 pt-4">
                    <ResultChart result={result} chartType={chartType} dimensionCount={resultDimCount} />
                  </div>
                )}

                {/* The table is the accessibility twin of the chart —
                    always rendered, never gated behind the tooltip. */}
                <div className="overflow-x-auto border-t border-border">
                  <table className="w-full text-sm" data-testid="result-table">
                    <thead className="border-b border-border bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
                      <tr>
                        {result.columns.map((c, i) => (
                          <th key={c} className={cn('px-4 py-2 font-medium', i < resultDimCount ? 'text-start' : 'text-end')}>
                            {humanize(c)}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-border">
                      {result.rows.map((row, i) => (
                        <tr key={i} className="hover:bg-muted/30">
                          {row.map((v, j) => (
                            // Columns are dims-first then measures (QueryResult
                            // contract) — j < resultDimCount identifies dims.
                            <td
                              key={j}
                              className={cn(
                                'px-4 py-1.5',
                                j >= resultDimCount && 'text-end tabular-nums text-muted-foreground',
                              )}
                            >
                              {j >= resultDimCount && isBytesMeasure(String(result.columns[j])) && typeof v === 'number'
                                ? formatFileSize(v)
                                : formatCell(v, j < resultDimCount)}
                            </td>
                          ))}
                        </tr>
                      ))}
                      {result.rows.length === 0 && (
                        <tr><td className="px-4 py-6 text-center text-muted-foreground" colSpan={result.columns.length}>No rows matched.</td></tr>
                      )}
                    </tbody>
                  </table>
                </div>
                <p className="border-t border-border px-4 py-2 text-xs text-muted-foreground">
                  {result.rows.length.toLocaleString('en-US')} row{result.rows.length === 1 ? '' : 's'}
                </p>
              </Card>
            </div>
          )}
        </div>
      </div>

      {saveOpen && (
        <SaveReportDialog
          existing={loadedReport}
          query={buildQuery()}
          chartType={chartType}
          onClose={() => setSaveOpen(false)}
          onSaved={(r) => {
            setSaveOpen(false)
            setLoadedReport(r)
            void qc.invalidateQueries({ queryKey: ['analytics-reports'] })
          }}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------
// building blocks
// ---------------------------------------------------------------------

// BuilderStep — one numbered section of the query builder. The number
// badge + hairline dividers are what make the rail read as a guided
// sequence instead of a pile of controls.
function BuilderStep({ n, title, hint, action, children }: {
  n: number
  title: string
  hint?: string
  action?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="px-4 py-3">
      <div className="mb-2 flex items-center justify-between">
        <p className="flex items-center gap-2 text-xs font-medium">
          <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/10 text-[10px] font-semibold text-primary">
            {n}
          </span>
          {title}
          {hint && <span className="font-normal text-muted-foreground">· {hint}</span>}
        </p>
        {action}
      </div>
      {children}
    </section>
  )
}

function Chip({ selected, onClick, children, testid }: {
  selected: boolean
  onClick: () => void
  children: React.ReactNode
  testid?: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={selected}
      className={cn(
        'rounded-full border px-2.5 py-0.5 text-xs transition-colors',
        selected
          ? 'border-primary/50 bg-primary/10 font-medium text-primary'
          : 'border-border text-muted-foreground hover:border-primary/30 hover:text-foreground',
      )}
      data-testid={testid}
    >
      {children}
    </button>
  )
}

// SummaryTiles — the headline row: total, group count, top group.
// Stat-tile contract from the dataviz method: sentence-case label,
// semibold auto-compact value, secondary context line.
function SummaryTiles({ result, dimCount, measureName }: {
  result: QueryResult
  dimCount: number
  measureName: string
}) {
  const stats = useMemo(() => {
    if (dimCount < 1 || result.rows.length === 0) return null
    let total = 0
    let top: { name: string; value: number } | null = null
    for (const row of result.rows) {
      const v = Number(row[dimCount] ?? 0)
      total += v
      if (!top || v > top.value) top = { name: row.slice(0, dimCount).map(dimensionLabel).join(' · '), value: v }
    }
    return { total, top }
  }, [result, dimCount])
  if (!stats) return null

  const share = stats.total > 0 && stats.top ? Math.round((stats.top.value / stats.total) * 100) : 0
  const groupLabel = humanize(String(result.columns[0] ?? 'group')).toLowerCase()
  return (
    <div className="grid gap-3 sm:grid-cols-3">
      <StatTile icon={Hash} label={`Total ${humanize(measureName).toLowerCase()}`} value={formatMeasure(measureName, stats.total)} />
      <StatTile icon={Layers} label="Groups" value={result.rows.length.toLocaleString('en-US')} sub={`distinct ${groupLabel} values`} />
      {stats.top && (
        <StatTile
          icon={Crown}
          label={`Top ${groupLabel}`}
          value={stats.top.name}
          sub={`${formatMeasure(measureName, stats.top.value)} · ${share}% of total`}
          valueTitle={stats.top.name}
        />
      )}
    </div>
  )
}

function StatTile({ icon: Icon, label, value, sub, valueTitle }: {
  icon: typeof Hash
  label: string
  value: string
  sub?: string
  valueTitle?: string
}) {
  return (
    <Card className="flex items-start gap-3 p-4">
      <span className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
        <Icon className="h-4 w-4" />
      </span>
      <div className="min-w-0">
        <p className="text-xs text-muted-foreground">{label}</p>
        <p className="truncate text-xl font-semibold" title={valueTitle}>{value}</p>
        {sub && <p className="truncate text-xs text-muted-foreground">{sub}</p>}
      </div>
    </Card>
  )
}

// ---------------------------------------------------------------------
// chart
// ---------------------------------------------------------------------

function ChartTip({ active, payload, label, measureName }: {
  active?: boolean
  payload?: Array<{ name?: string; value?: number; payload?: { name: string; share?: number } }>
  label?: string
  measureName: string
}) {
  if (!active || !payload?.length) return null
  const p = payload[0]
  const name = label ?? p.payload?.name ?? ''
  const share = p.payload?.share
  return (
    <div className="rounded-md bg-card px-3 py-2 text-xs shadow-neu">
      <p className="font-medium text-foreground">{name}</p>
      <p className="mt-0.5 text-muted-foreground">
        {humanize(measureName)}: <span className="font-medium text-foreground">{formatMeasure(measureName, Number(p.value ?? 0))}</span>
        {share != null && <span> · {share}%</span>}
      </p>
    </div>
  )
}

const MAX_PIE_SEGMENTS = 6

function ResultChart({ result, chartType, dimensionCount }: {
  result: QueryResult
  chartType: ChartType
  dimensionCount: number
}) {
  // First dimension(s) become the label; the first measure the value.
  const raw = useMemo(() => result.rows.map((row) => ({
    name: row.slice(0, dimensionCount).map(dimensionLabel).join(' · '),
    value: Number(row[dimensionCount] ?? 0),
  })), [result, dimensionCount])
  const measureName = result.columns[dimensionCount] ?? 'value'

  // Bars and donuts are magnitude comparisons → sort descending. Lines
  // keep the backend's (chronological) order.
  const sorted = useMemo(() => [...raw].sort((a, b) => b.value - a.value), [raw])

  if (chartType === 'pie') {
    // ≤ 6 segments; the tail folds into "Other" (never a 9th hue).
    const head = sorted.slice(0, MAX_PIE_SEGMENTS - 1)
    const tail = sorted.slice(MAX_PIE_SEGMENTS - 1)
    const data = tail.length > 1
      ? [...head, { name: 'Other', value: tail.reduce((s, d) => s + d.value, 0) }]
      : sorted
    const total = data.reduce((s, d) => s + d.value, 0)
    const withShare = data.map((d) => ({ ...d, share: total > 0 ? Math.round((d.value / total) * 100) : 0 }))
    return (
      <ResponsiveContainer width="100%" height="100%">
        <PieChart>
          <Pie
            data={withShare}
            dataKey="value"
            nameKey="name"
            innerRadius="55%"
            outerRadius="85%"
            // 2px surface gap between segments — the spacer, not a border.
            stroke="hsl(var(--card))"
            strokeWidth={2}
            startAngle={90}
            endAngle={-270}
          >
            {withShare.map((d, i) => (
              <Cell key={d.name} fill={d.name === 'Other' ? INK_MUTED : CHART_COLORS[i % CHART_COLORS.length]} />
            ))}
          </Pie>
          <ChartTooltip content={<ChartTip measureName={measureName} />} />
          <Legend
            iconType="circle"
            iconSize={8}
            formatter={(v: string) => <span className="text-xs" style={{ color: '#52514e' }}>{v}</span>}
          />
        </PieChart>
      </ResponsiveContainer>
    )
  }

  if (chartType === 'line') {
    return (
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={raw} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
          <CartesianGrid vertical={false} stroke={GRID} />
          <XAxis dataKey="name" tickLine={false} axisLine={{ stroke: AXIS }} tick={{ fontSize: 11, fill: INK_MUTED }} />
          <YAxis tickLine={false} axisLine={false} width={52} tick={{ fontSize: 11, fill: INK_MUTED }} tickFormatter={(v: number) => formatAxis(measureName, v)} />
          <ChartTooltip content={<ChartTip measureName={measureName} />} />
          <Line
            type="monotone"
            dataKey="value"
            name={measureName}
            stroke={SERIES}
            strokeWidth={2}
            dot={false}
            // ≥8px marker with a 2px surface ring where it sits on the line.
            activeDot={{ r: 4.5, strokeWidth: 2, stroke: 'hsl(var(--card))' }}
          />
        </LineChart>
      </ResponsiveContainer>
    )
  }

  return (
    <ResponsiveContainer width="100%" height="100%">
      <BarChart data={sorted} margin={{ top: 8, right: 16, bottom: 0, left: 0 }} barCategoryGap="25%">
        <CartesianGrid vertical={false} stroke={GRID} />
        <XAxis
          dataKey="name"
          tickLine={false}
          axisLine={{ stroke: AXIS }}
          tick={{ fontSize: 11, fill: INK_MUTED }}
          tickFormatter={(v: string) => (v.length > 14 ? `${v.slice(0, 13)}…` : v)}
        />
        <YAxis tickLine={false} axisLine={false} width={52} tick={{ fontSize: 11, fill: INK_MUTED }} tickFormatter={(v: number) => formatAxis(measureName, v)} />
        <ChartTooltip content={<ChartTip measureName={measureName} />} cursor={{ fill: 'rgba(0,0,0,0.04)' }} />
        {/* Single series → one hue (never a color per bar), thin marks,
            4px rounded data-end, square at the baseline. */}
        <Bar dataKey="value" name={measureName} fill={SERIES} maxBarSize={24} radius={[4, 4, 0, 0]} />
      </BarChart>
    </ResponsiveContainer>
  )
}

// ---------------------------------------------------------------------
// save + schedule dialog
// ---------------------------------------------------------------------

const INTERVALS: Array<{ label: string; minutes: number }> = [
  { label: 'Hourly', minutes: 60 },
  { label: 'Daily', minutes: 1440 },
  { label: 'Weekly', minutes: 10080 },
]

function SaveReportDialog({ existing, query, chartType, onClose, onSaved }: {
  existing: SavedReport | null
  query: AnalyticsQuery
  chartType: ChartType
  onClose: () => void
  onSaved: (r: SavedReport) => void
}) {
  const [name, setName] = useState(existing?.name ?? '')
  const [description, setDescription] = useState(existing?.description ?? '')
  const [scheduled, setScheduled] = useState(existing?.schedule_enabled ?? false)
  const [intervalMinutes, setIntervalMinutes] = useState(existing?.schedule_interval_minutes || 1440)
  const [cron, setCron] = useState(existing?.schedule_cron ?? '')
  const [channels, setChannels] = useState<string[]>(existing?.channels ?? ['in_app'])

  const save = useAppMutation({
    mutationFn: () => {
      const input: SaveReportInput = {
        name, description, query, chart_type: chartType,
        schedule_enabled: scheduled,
        schedule_cron: cron.trim(),
        schedule_interval_minutes: cron.trim() ? 0 : intervalMinutes,
        channels,
      }
      return existing ? updateReport(existing.id, input) : createReport(input)
    },
    onSuccess: (r) => { toast.success('Report saved'); onSaved(r) },
    defaultErrorMessage: 'Could not save report',
  })

  const toggleChannel = (c: string) => {
    setChannels((cur) => (cur.includes(c) ? cur.filter((x) => x !== c) : [...cur, c]))
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={existing ? 'Save changes' : 'Save report'}>
      <div className="space-y-3">
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} data-testid="report-name" />
        <Input label="Description" value={description} onChange={(e) => setDescription(e.target.value)} />

        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={scheduled} onChange={(e) => setScheduled(e.target.checked)} data-testid="report-scheduled" />
          Schedule delivery (notifies you with a link when it runs)
        </label>

        {scheduled && (
          <div className="space-y-2 rounded-md bg-muted p-3 shadow-neu-inset">
            <label className="block text-sm">
              <span className="mb-1 block text-xs font-medium">Frequency</span>
              <select
                value={cron.trim() ? 'cron' : String(intervalMinutes)}
                onChange={(e) => {
                  if (e.target.value === 'cron') setCron('0 9 * * 1')
                  else { setCron(''); setIntervalMinutes(Number(e.target.value)) }
                }}
                className="w-full rounded-md border border-input bg-muted px-2 py-1.5 shadow-neu-inset"
              >
                {INTERVALS.map((i) => <option key={i.minutes} value={i.minutes}>{i.label}</option>)}
                <option value="cron">Custom cron…</option>
              </select>
            </label>
            {cron.trim() !== '' && (
              <Input
                label="Cron (UTC, e.g. '0 9 * * 1-5' = 9am weekdays)"
                value={cron}
                onChange={(e) => setCron(e.target.value)}
              />
            )}
            <div>
              <p className="mb-1 text-xs font-medium">Notify via</p>
              <div className="flex gap-3 text-sm">
                {['in_app', 'email', 'digest'].map((c) => (
                  <label key={c} className="flex items-center gap-1.5">
                    <input type="checkbox" checked={channels.includes(c)} onChange={() => toggleChannel(c)} />
                    {c}
                  </label>
                ))}
              </div>
            </div>
          </div>
        )}

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button
            onClick={() => save.mutate()}
            disabled={save.isPending || !name.trim() || (scheduled && channels.length === 0)}
            data-testid="report-save-confirm"
          >
            Save
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
