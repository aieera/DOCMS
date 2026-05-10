import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { AlertTriangle, Search, Shield, Clock, Hash } from 'lucide-react'

import {
  runFederatedSearch,
  listMyFederatedAudit,
  type FederatedSearchResult,
  type FederatedAuditRecord,
} from '@/api/federated-search'
import { PageHeader } from '@/components/shared/PageHeader'
import { Input } from '@/components/ui/shadcn/input'
import { Textarea } from '@/components/ui/shadcn/textarea'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { Badge } from '@/components/ui/Badge'

// ADR 0069 — platform-admin federated search.
//
// Distinct visual framing from the rest of the admin app: red border,
// explicit"cross-tenant" banner at the top, required reason field
// validated client-side AND server-side. The page never auto-runs
// the query on URL state — every search is an explicit click so
//"I just landed on the URL" can never trigger a federated query.

const MIN_REASON_CHARS = 10
const ROW_OUTCOME_COLOR: Record<FederatedAuditRecord['outcome'], string> = {
  success:       'success',
  denied_perm:   'danger',
  denied_quota:  'warning',
  error:         'warning',
}

function SupportSearchPage() {
  const qc = useQueryClient()
  const [query, setQuery] = useState('')
  const [reason, setReason] = useState('')
  const [result, setResult] = useState<FederatedSearchResult | null>(null)

  const runMut = useMutation({
    mutationFn: () => runFederatedSearch({ query, reason }),
    onSuccess: (r) => {
      setResult(r)
      qc.invalidateQueries({ queryKey: ['my-federated-audit'] })
      toast.success(`${r.total_hits} hits across ${r.tenants_with_hits} tenant${r.tenants_with_hits === 1 ? '' : 's'}`)
    },
    onError: (err: { response?: { status?: number; data?: { error?: string } } }) => {
      const status = err.response?.status
      const detail = err.response?.data?.error
      // The audit row was already written server-side for every
      // denial path; refresh the recent-list so the admin sees
      // their attempt land alongside successes.
      qc.invalidateQueries({ queryKey: ['my-federated-audit'] })
      if (status === 403) {
        toast.error('You are not a platform admin')
      } else if (status === 429) {
        toast.error('Daily limit reached (100/day)')
      } else if (status === 400) {
        toast.error(detail ?? 'Invalid request')
      } else {
        toast.error('Federated search failed')
      }
    },
  })

  const { data: auditRows } = useQuery({
    queryKey: ['my-federated-audit'],
    queryFn: () => listMyFederatedAudit(20),
    staleTime: 10_000,
  })

  const reasonOK = reason.trim().length >= MIN_REASON_CHARS
  const queryOK = query.trim().length > 0
  const canSearch = reasonOK && queryOK && !runMut.isPending

  return (
    <div className="mx-auto max-w-4xl">
      <PageHeader
        title="Cross-tenant support search"
        description="Platform-admin only. Every query is audited with full payload + your identity. ADR 0069."
      />

      {/* Red framing — load-bearing. Different visually from every
          other admin page in the app so support engineers always
          know which surface they're on. */}
      <div
        className="mb-4 rounded-md border-2 border-red-500 bg-destructive/10 p-4 dark:bg-red-950"
        data-testid="cross-tenant-banner"
      >
        <div className="flex items-start gap-2 text-red-900 dark:text-red-200">
          <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0" />
          <div className="space-y-1 text-sm">
            <p className="font-semibold">⚠️ This search BYPASSES tenant isolation.</p>
            <p>
              Every query is recorded with your user id, the full payload, and the reason
              you provide below. Limit: <strong>100 queries / UTC day</strong>. Use this
              ONLY for documented support / incident-response cases.
            </p>
          </div>
        </div>
      </div>

      <div
        className="rounded-md border-2 border-red-300 bg-card p-4 dark:border-red-700"
        data-testid="federated-form"
      >
        <Textarea
          label="Reason (required, ≥10 chars)"
          placeholder="e.g. Incident SUP-1234 — verifying suspected phishing payload was not ingested."
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          rows={3}
          data-testid="federated-reason"
        />
        {reason.length > 0 && !reasonOK && (
          <p className="mt-1 text-xs text-destructive">
            Need at least {MIN_REASON_CHARS} characters. Saying &ldquo;test&rdquo; doesn&apos;t pass compliance review.
          </p>
        )}

        <div className="mt-3">
          <Input
            icon={<Search className="h-4 w-4" />}
            label="Query"
            placeholder="Search term — title, content, tag…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            data-testid="federated-query"
          />
        </div>

        <div className="mt-3 flex justify-end">
          <Button
            onClick={() => runMut.mutate()}
            disabled={!canSearch}
            data-testid="federated-submit"
          >
            {runMut.isPending ? <Spinner className="h-4 w-4" /> : <Search className="h-4 w-4" />}
            Run cross-tenant search
          </Button>
        </div>
      </div>

      {result && (
        <ResultPanel result={result} />
      )}

      <div className="mt-6">
        <h3 className="mb-2 flex items-center gap-2 text-sm font-semibold">
          <Clock className="h-4 w-4" />
          My recent federated queries
        </h3>
        <p className="mb-3 text-xs text-muted-foreground">
          Every cross-tenant search you run is recorded here, including denied attempts.
          Compliance reviews this log monthly.
        </p>
        <div className="space-y-2" data-testid="audit-list">
          {(auditRows ?? []).length === 0 ? (
            <div className="rounded-md border border-dashed border-border p-4 text-center text-sm text-muted-foreground">
              No queries yet.
            </div>
          ) : (
            (auditRows ?? []).map((row) => <AuditRow key={row.id} row={row} />)
          )}
        </div>
      </div>
    </div>
  )
}

function ResultPanel({ result }: { result: FederatedSearchResult }) {
  const tenants = Object.entries(result.results_by_tenant)
  return (
    <div className="mt-6 space-y-3" data-testid="federated-results">
      <div className="flex items-center justify-between rounded-md border border-border bg-card p-3">
        <div className="flex items-center gap-2 text-sm">
          <Shield className="h-4 w-4" />
          <span><strong>{result.total_hits}</strong> hits across <strong>{result.tenants_with_hits}</strong> tenant{result.tenants_with_hits === 1 ? '' : 's'}</span>
        </div>
        <span className="text-xs text-muted-foreground">
          {result.latency_ms}ms · audit id <span className="font-mono">{result.audit_id || '(written)'}</span>
        </span>
      </div>
      {tenants.map(([tenantID, hits]) => (
        <div
          key={tenantID}
          className="rounded-md border border-border bg-card p-3"
          data-testid={`tenant-bucket-${tenantID}`}
        >
          <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
            <Hash className="h-3 w-3" />
            <span>tenant <span className="font-mono">{tenantID.slice(0, 8)}…</span></span>
            <span>· {hits.length} hit{hits.length === 1 ? '' : 's'}</span>
          </div>
          <ul className="space-y-1">
            {hits.map((hit) => (
              <li key={hit.document_id} className="flex items-center justify-between text-sm">
                <span className="truncate">{hit.title || hit.document_id}</span>
                <span className="ml-2 shrink-0 text-xs text-muted-foreground">
                  {hit.mime_type} · {hit.lifecycle_state}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  )
}

function AuditRow({ row }: { row: FederatedAuditRecord }) {
  const variant = ROW_OUTCOME_COLOR[row.outcome] || 'default'
  return (
    <div
      className="rounded-md border border-border bg-card p-3 text-sm"
      data-testid={`audit-row-${row.id}`}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <Badge variant={variant as 'success' | 'danger' | 'warning' | 'default'}>
              {row.outcome}
            </Badge>
            <span className="truncate text-xs text-muted-foreground">
              {new Date(row.created_at).toLocaleString()} · {row.latency_ms}ms
            </span>
          </div>
          <p className="mt-1 truncate font-medium">{row.query_payload.query || <em>(no query)</em>}</p>
          <p className="mt-1 text-xs text-muted-foreground">
            <strong>Reason:</strong> {row.reason}
          </p>
          {row.outcome === 'success' && row.results_summary.total_hits != null && (
            <p className="mt-1 text-xs">
              {row.results_summary.total_hits} hits across {row.results_summary.tenants_with_hits} tenants
            </p>
          )}
          {row.error_kind && (
            <p className="mt-1 text-xs text-destructive">Error: {row.error_kind}</p>
          )}
        </div>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/platform/support-search')({
  component: SupportSearchPage,
})
