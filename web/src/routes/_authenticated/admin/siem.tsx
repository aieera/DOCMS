import { PasswordInput } from '@/components/ui/PasswordInput'
import { useState } from 'react'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Plus, Trash2, Send, CheckCircle2, XCircle, Radio } from 'lucide-react'

import {
  listSinks, createSink, updateSink, deleteSink, testSink,
  type Sink, type SinkType, type SinkInput,
} from '@/api/siem'
import { useAppMutation } from '@/hooks/useAppMutation'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'

const TYPES: { value: SinkType; label: string; endpointHint: string }[] = [
  { value: 'splunk_hec', label: 'Splunk HEC', endpointHint: 'https://splunk:8088' },
  { value: 'sentinel_hec', label: 'Microsoft Sentinel', endpointHint: 'https://<dce>.ingest.monitor.azure.com/…' },
  { value: 'syslog', label: 'Syslog (RFC 5424)', endpointHint: 'host:514  or  udp://host:514' },
]

export function SIEMPage() {
  const qc = useQueryClient()
  const { data: sinks = [] } = useQuery({ queryKey: ['siem-sinks'], queryFn: listSinks })
  const [draft, setDraft] = useState<SinkInput>({ name: '', type: 'splunk_hec', endpoint: '', token: '', enabled: true })

  const create = useAppMutation({
    mutationFn: () => createSink(draft),
    onSuccess: () => {
      toast.success('Sink created')
      setDraft({ name: '', type: 'splunk_hec', endpoint: '', token: '', enabled: true })
      qc.invalidateQueries({ queryKey: ['siem-sinks'] })
    },
    defaultErrorMessage: 'Could not create sink',
  })
  const toggle = useAppMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => updateSink(id, { enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['siem-sinks'] }),
    defaultErrorMessage: 'Could not update sink',
  })
  const remove = useAppMutation({
    mutationFn: (id: string) => deleteSink(id),
    onSuccess: () => { toast.success('Sink deleted'); qc.invalidateQueries({ queryKey: ['siem-sinks'] }) },
    defaultErrorMessage: 'Could not delete sink',
  })
  const test = useAppMutation({
    mutationFn: (id: string) => testSink(id),
    onSuccess: (res) => {
      if (res.ok) toast.success('Test event delivered')
      else toast.error(`Test failed: ${res.error ?? 'unknown error'}`)
      qc.invalidateQueries({ queryKey: ['siem-sinks'] })
    },
    defaultErrorMessage: 'Test failed',
  })

  const hint = TYPES.find((t) => t.value === draft.type)?.endpointHint

  return (
    <div className="mx-auto max-w-4xl p-6">
      <PageHeader
        title="SIEM forwarding"
        description="Forward normalised audit + domain events to your SIEM (syslog, Splunk HEC, Microsoft Sentinel). Delivery retries then routes to a DLQ on persistent failure."
      />

      <div className="mt-6 grid gap-2 rounded-lg border border-border bg-card p-4 sm:grid-cols-6" data-testid="sink-form">
        <input className="rounded border border-border bg-background px-2 py-1.5 text-sm sm:col-span-2"
          placeholder="Sink name" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
        <select className="rounded border border-border bg-background px-2 py-1.5 text-sm"
          value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value as SinkType })}>
          {TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
        </select>
        <input className="rounded border border-border bg-background px-2 py-1.5 text-sm sm:col-span-2"
          placeholder={hint} value={draft.endpoint} onChange={(e) => setDraft({ ...draft, endpoint: e.target.value })} />
        <PasswordInput className="h-auto rounded border-border px-2 py-1.5 text-sm"
          placeholder="token" value={draft.token} onChange={(e) => setDraft({ ...draft, token: e.target.value })} />
        <Button size="sm" className="sm:col-span-6 sm:w-40" disabled={!draft.name || !draft.endpoint || create.isPending}
          onClick={() => create.mutate(undefined)} data-testid="sink-create">
          <Plus className="h-3.5 w-3.5" /> Add sink
        </Button>
      </div>

      <div className="mt-4 space-y-2">
        {sinks.map((s) => <SinkRow key={s.id} sink={s}
          onToggle={() => toggle.mutate({ id: s.id, enabled: !s.enabled })}
          onTest={() => test.mutate(s.id)} onDelete={() => remove.mutate(s.id)}
          testing={test.isPending} />)}
        {sinks.length === 0 && (
          <p className="rounded border border-dashed border-border p-4 text-center text-sm text-muted-foreground">
            No SIEM sinks configured. Add one above and click “Send test”.
          </p>
        )}
      </div>
    </div>
  )
}

function SinkRow({ sink, onToggle, onTest, onDelete, testing }: {
  sink: Sink; onToggle: () => void; onTest: () => void; onDelete: () => void; testing: boolean
}) {
  const total = sink.delivered_count + sink.failed_count
  const rate = total > 0 ? Math.round((sink.delivered_count / total) * 100) : 100
  return (
    <div className="rounded-lg border border-border p-3" data-testid={`sink-${sink.id}`}>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Radio className={`h-4 w-4 ${sink.enabled ? 'text-emerald-500' : 'text-muted-foreground'}`} />
        <span className="font-medium">{sink.name}</span>
        <span className="rounded bg-muted px-1.5 py-0.5 text-xs">{sink.type}</span>
        <span className="text-xs text-muted-foreground">{sink.endpoint}</span>
        <span className="ms-auto flex items-center gap-3">
          <span className="inline-flex items-center gap-1 text-xs text-emerald-600"><CheckCircle2 className="h-3.5 w-3.5" />{sink.delivered_count}</span>
          <span className="inline-flex items-center gap-1 text-xs text-red-600"><XCircle className="h-3.5 w-3.5" />{sink.failed_count}</span>
          <span className="text-xs text-muted-foreground">{rate}% ok</span>
        </span>
      </div>
      {sink.last_error && (
        <p className="mt-1 text-xs text-red-600" title={sink.last_error}>last error: {sink.last_error.slice(0, 80)}</p>
      )}
      <div className="mt-2 flex items-center gap-2">
        <Button size="sm" variant="outline" className="h-7" onClick={onTest} disabled={testing} data-testid={`test-${sink.id}`}>
          <Send className="h-3.5 w-3.5" /> Send test
        </Button>
        <Button size="sm" variant="ghost" className="h-7" onClick={onToggle}>
          {sink.enabled ? 'Disable' : 'Enable'}
        </Button>
        <Button size="sm" variant="ghost" className="h-7 text-destructive" onClick={onDelete} data-testid={`delete-${sink.id}`}>
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/audit?tab=forwarding). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/siem')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/audit', search: { tab: 'forwarding' }, replace: true })
  },
})
