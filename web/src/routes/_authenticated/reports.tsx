// Report builder (ADR 0119): pick dataset → dimensions/measures/filters,
// run against the governed query API, chart + table, save + schedule,
// export CSV / print-to-PDF.
import { createFileRoute } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { BarChart3, Play, Save, Trash2, Download, Printer, CalendarClock, Plus, X } from 'lucide-react'
import {
  BarChart, Bar, LineChart, Line, PieChart, Pie, Cell,
  XAxis, YAxis, Tooltip as ChartTooltip, ResponsiveContainer, Legend,
} from 'recharts'

import {
  listDatasets, runQuery, listReports, createReport, updateReport, deleteReport, toCSV,
  type AnalyticsQuery, type QueryResult, type SavedReport, type SaveReportInput,
} from '@/api/analytics'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/shadcn/input'
import { EmptyState } from '@/components/ui/EmptyState'

export const Route = createFileRoute('/_authenticated/reports')({
  component: ReportsPage,
})

type ChartType = 'table' | 'bar' | 'line' | 'pie'

const CHART_COLORS = ['#1E40AF', '#0891b2', '#16a34a', '#ca8a04', '#dc2626', '#7c3aed', '#db2777', '#475569']

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

  const run = useAppMutation({
    mutationFn: async () => {
      const q = buildQuery()
      const res = await runQuery(q)
      return { res, dims: q.dimensions.length }
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

  const loadReport = (r: SavedReport) => {
    setLoadedReport(r)
    setDataset(r.query.dataset)
    setDimensions(r.query.dimensions ?? [])
    setMeasures(r.query.measures ?? [])
    setFilters(Object.entries(r.query.filters ?? {}).map(([field, vals]) => ({ field, values: vals.join(', ') })))
    setFrom(r.query.time_range?.from ? r.query.time_range.from.slice(0, 10) : '')
    setTo(r.query.time_range?.to ? r.query.time_range.to.slice(0, 10) : '')
    setChartType(r.chart_type)
    setResult(null)
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

  return (
    <div className="p-6">
      <PageHeader
        title="Reports"
        description="Governed analytics over your documents, versions and tasks"
      />

      <div className="grid gap-4 lg:grid-cols-[280px_1fr]">
        {/* ---- left rail: saved reports + builder controls ---------- */}
        <div className="space-y-4">
          <div className="rounded-lg border border-border bg-card p-3">
            <p className="mb-2 text-xs font-semibold uppercase text-muted-foreground">Saved reports</p>
            {(reports ?? []).length === 0 && (
              <p className="text-xs text-muted-foreground">None yet — build one and Save.</p>
            )}
            {(reports ?? []).map((r) => (
              <div key={r.id} className="mb-1 flex items-center gap-1">
                <button
                  type="button"
                  onClick={() => loadReport(r)}
                  className={`flex-1 truncate rounded px-2 py-1 text-start text-sm hover:bg-muted ${loadedReport?.id === r.id ? 'bg-muted font-medium' : ''}`}
                  data-testid={`report-item-${r.id}`}
                >
                  {r.name}
                </button>
                {r.schedule_enabled && <CalendarClock className="h-3.5 w-3.5 shrink-0 text-primary" />}
                <button type="button" onClick={() => { if (confirm(`Delete report "${r.name}"?`)) del.mutate(r.id) }}>
                  <Trash2 className="h-3.5 w-3.5 text-destructive" />
                </button>
              </div>
            ))}
          </div>

          <div className="space-y-3 rounded-lg border border-border bg-card p-3">
            <label className="block text-sm">
              <span className="mb-1 block text-xs font-medium">Dataset</span>
              <select
                value={dataset}
                onChange={(e) => {
                  setDataset(e.target.value)
                  setDimensions([])
                  setMeasures(['count'])
                  setFilters([])
                  setResult(null)
                  setLoadedReport(null)
                }}
                className="w-full rounded-md border border-border bg-background px-2 py-1.5"
                data-testid="dataset-select"
              >
                {(datasets ?? []).map((d) => <option key={d.name} value={d.name}>{d.name}</option>)}
              </select>
            </label>

            <div>
              <p className="mb-1 text-xs font-medium">Dimensions (group by, max 3)</p>
              <div className="flex flex-wrap gap-1.5">
                {(ds?.dimensions ?? []).map((d) => (
                  <button
                    key={d}
                    type="button"
                    onClick={() => toggle(dimensions, setDimensions, d, 3)}
                    className={`rounded-full border px-2 py-0.5 text-xs ${dimensions.includes(d) ? 'border-primary bg-primary text-primary-foreground' : 'border-border'}`}
                    data-testid={`dim-${d}`}
                  >
                    {d}
                  </button>
                ))}
              </div>
            </div>

            <div>
              <p className="mb-1 text-xs font-medium">Measures</p>
              <div className="flex flex-wrap gap-1.5">
                {(ds?.measures ?? []).map((m) => (
                  <button
                    key={m}
                    type="button"
                    onClick={() => toggle(measures, setMeasures, m)}
                    className={`rounded-full border px-2 py-0.5 text-xs ${measures.includes(m) ? 'border-primary bg-primary text-primary-foreground' : 'border-border'}`}
                    data-testid={`measure-${m}`}
                  >
                    {m}
                  </button>
                ))}
              </div>
            </div>

            <div>
              <div className="mb-1 flex items-center justify-between">
                <p className="text-xs font-medium">Filters</p>
                <Button size="sm" variant="ghost" aria-label="Add filter" onClick={() => setFilters((f) => [...f, { field: ds?.dimensions[0] ?? '', values: '' }])}>
                  <Plus className="h-3 w-3" />
                </Button>
              </div>
              {filters.map((f, i) => (
                <div key={i} className="mb-1 flex items-center gap-1">
                  <select
                    value={f.field}
                    onChange={(e) => setFilters((fs) => fs.map((x, xi) => (xi === i ? { ...x, field: e.target.value } : x)))}
                    className="w-28 rounded border border-border bg-background px-1 py-1 text-xs"
                  >
                    {(ds?.dimensions ?? []).map((d) => <option key={d} value={d}>{d}</option>)}
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
            </div>

            <div className="flex gap-2">
              <label className="flex-1 text-xs">
                From
                <Input type="date" value={from} onChange={(e) => setFrom(e.target.value)} className="h-8" />
              </label>
              <label className="flex-1 text-xs">
                To
                <Input type="date" value={to} onChange={(e) => setTo(e.target.value)} className="h-8" />
              </label>
            </div>

            <Button
              className="w-full"
              onClick={() => run.mutate()}
              disabled={run.isPending || measures.length === 0}
              data-testid="run-query"
            >
              <Play className="h-4 w-4" /> Run
            </Button>
          </div>
        </div>

        {/* ---- results ---------------------------------------------- */}
        <div className="min-w-0 space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            {(['table', 'bar', 'line', 'pie'] as ChartType[]).map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => setChartType(t)}
                className={`rounded-md border px-3 py-1 text-sm ${chartType === t ? 'border-primary bg-primary text-primary-foreground' : 'border-border'}`}
                data-testid={`chart-${t}`}
              >
                {t}
              </button>
            ))}
            <div className="ms-auto flex gap-2">
              <Button variant="outline" size="sm" onClick={exportCSV} disabled={!result} data-testid="export-csv">
                <Download className="h-4 w-4" /> CSV
              </Button>
              <Button variant="outline" size="sm" onClick={() => window.print()} disabled={!result}>
                <Printer className="h-4 w-4" /> PDF
              </Button>
              <Button size="sm" onClick={() => setSaveOpen(true)} disabled={measures.length === 0} data-testid="save-report">
                <Save className="h-4 w-4" /> {loadedReport ? 'Save changes' : 'Save'}
              </Button>
            </div>
          </div>

          {!result ? (
            <EmptyState
              icon={<BarChart3 className="h-8 w-8" />}
              title="Run a query"
              description="Pick dimensions and measures, then Run. Save it to schedule delivery."
            />
          ) : (
            <>
              {chartType !== 'table' && resultDimCount >= 1 && (
                <div className="h-80 rounded-lg border border-border bg-card p-3">
                  <ResultChart result={result} chartType={chartType} dimensionCount={resultDimCount} />
                </div>
              )}
              <div className="overflow-x-auto rounded-lg border border-border">
                <table className="w-full text-sm" data-testid="result-table">
                  <thead>
                    <tr className="border-b border-border bg-muted/50 text-start">
                      {result.columns.map((c) => (
                        <th key={c} className="px-3 py-2 text-start font-medium">{c}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {result.rows.map((row, i) => (
                      <tr key={i} className="border-b border-border last:border-0">
                        {row.map((v, j) => (
                          <td key={j} className="px-3 py-1.5">{String(v ?? '')}</td>
                        ))}
                      </tr>
                    ))}
                    {result.rows.length === 0 && (
                      <tr><td className="px-3 py-4 text-muted-foreground" colSpan={result.columns.length}>No rows.</td></tr>
                    )}
                  </tbody>
                </table>
              </div>
              <p className="text-xs text-muted-foreground">{result.rows.length} rows</p>
            </>
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
// chart
// ---------------------------------------------------------------------

function ResultChart({ result, chartType, dimensionCount }: {
  result: QueryResult
  chartType: ChartType
  dimensionCount: number
}) {
  // First dimension(s) become the label; the first measure the value.
  const data = useMemo(() => result.rows.map((row) => ({
    name: row.slice(0, dimensionCount).map((v) => String(v ?? '')).join(' · '),
    value: Number(row[dimensionCount] ?? 0),
  })), [result, dimensionCount])
  const measureName = result.columns[dimensionCount] ?? 'value'

  if (chartType === 'pie') {
    return (
      <ResponsiveContainer width="100%" height="100%">
        <PieChart>
          <Pie data={data} dataKey="value" nameKey="name" label>
            {data.map((_, i) => <Cell key={i} fill={CHART_COLORS[i % CHART_COLORS.length]} />)}
          </Pie>
          <ChartTooltip />
          <Legend />
        </PieChart>
      </ResponsiveContainer>
    )
  }
  if (chartType === 'line') {
    return (
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data}>
          <XAxis dataKey="name" tick={{ fontSize: 11 }} />
          <YAxis tick={{ fontSize: 11 }} />
          <ChartTooltip />
          <Line type="monotone" dataKey="value" name={measureName} stroke={CHART_COLORS[0]} dot={false} />
        </LineChart>
      </ResponsiveContainer>
    )
  }
  return (
    <ResponsiveContainer width="100%" height="100%">
      <BarChart data={data}>
        <XAxis dataKey="name" tick={{ fontSize: 11 }} />
        <YAxis tick={{ fontSize: 11 }} />
        <ChartTooltip />
        <Bar dataKey="value" name={measureName} fill={CHART_COLORS[0]} />
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
          <div className="space-y-2 rounded-md border border-border bg-muted/30 p-3">
            <label className="block text-sm">
              <span className="mb-1 block text-xs font-medium">Frequency</span>
              <select
                value={cron.trim() ? 'cron' : String(intervalMinutes)}
                onChange={(e) => {
                  if (e.target.value === 'cron') setCron('0 9 * * 1')
                  else { setCron(''); setIntervalMinutes(Number(e.target.value)) }
                }}
                className="w-full rounded-md border border-border bg-background px-2 py-1.5"
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
