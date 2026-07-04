import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { toast } from 'sonner'
import { CheckCircle2, XCircle, ShieldQuestion, Download, FileBadge } from 'lucide-react'

import {
  listStandards, getCoverage, verifyAuditIntegrity, accessionExport,
  type ReqStatus,
} from '@/api/records'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'

// Records-management certification dashboard: pick a standard, see the
// requirement → SeDoc-mechanism checklist with live pass/fail, the coverage
// score, and export a certification evidence pack (coverage + audit hash-chain
// proof + accession manifest) in one click.

function StatusIcon({ status }: { status: ReqStatus }) {
  if (status === 'pass') return <CheckCircle2 className="h-4 w-4 text-emerald-500" />
  if (status === 'fail') return <XCircle className="h-4 w-4 text-red-500" />
  return <ShieldQuestion className="h-4 w-4 text-blue-500" />
}

function downloadJSON(filename: string, obj: unknown) {
  const blob = new Blob([JSON.stringify(obj, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

export function CertificationDashboard() {
  const { data: standards = [] } = useQuery({ queryKey: ['record-standards'], queryFn: listStandards })
  const [standardId, setStandardId] = useState('dod5015.2')
  const [exporting, setExporting] = useState(false)

  const { data: coverage, isLoading } = useQuery({
    queryKey: ['record-coverage', standardId],
    queryFn: () => getCoverage(standardId),
  })

  const exportEvidence = async () => {
    setExporting(true)
    try {
      // Reuse records coverage + the audit hash-chain proof + the accession
      // manifest. Each piece is fetched live so the pack reflects "now".
      const [cov, audit, manifest] = await Promise.all([
        getCoverage(standardId),
        verifyAuditIntegrity().catch(() => ({ error: 'audit verify-integrity unavailable' })),
        accessionExport(false).catch(() => null),
      ])
      const pack = {
        kind: 'sedoc.records.certification-evidence.v1',
        generated_at: new Date().toISOString(),
        standard: standardId,
        coverage: cov,
        audit_integrity: audit,
        accession_manifest: manifest,
      }
      downloadJSON(`records-certification-${standardId}-${Date.now()}.json`, pack)
      toast.success('Certification evidence pack exported')
    } catch (e) {
      toast.error(`Export failed: ${String(e)}`)
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className="space-y-4" data-testid="certification-dashboard">
      <div className="flex flex-wrap items-center gap-3">
        <label className="text-sm font-medium">Standard</label>
        <select
          className="rounded border border-border bg-background px-2 py-1 text-sm"
          value={standardId}
          onChange={(e) => setStandardId(e.target.value)}
          data-testid="standard-selector"
        >
          {standards.map((s) => (
            <option key={s.id} value={s.id}>{s.name} ({s.requirements})</option>
          ))}
        </select>
        <Button size="sm" onClick={exportEvidence} disabled={exporting} className="ms-auto" data-testid="export-evidence">
          {exporting ? <Spinner className="h-3 w-3" /> : <Download className="h-3 w-3" />}
          Export evidence pack
        </Button>
      </div>

      {isLoading ? (
        <div className="flex items-center gap-2 p-4 text-sm text-muted-foreground"><Spinner className="h-4 w-4" /> Evaluating coverage…</div>
      ) : coverage ? (
        <>
          <div className="flex flex-wrap items-center gap-4 rounded-lg border border-border p-4">
            <div className="flex items-center gap-2">
              <FileBadge className="h-8 w-8 text-violet-500" />
              <div>
                <div className="text-2xl font-bold tabular-nums">{coverage.coverage_pct}%</div>
                <div className="text-xs text-muted-foreground">coverage</div>
              </div>
            </div>
            <div className="flex gap-4 text-sm">
              <span className="text-emerald-600">{coverage.passed} pass</span>
              <span className="text-blue-600">{coverage.attested} attested</span>
              <span className={coverage.gaps > 0 ? 'text-red-600' : 'text-muted-foreground'}>{coverage.gaps} gaps</span>
            </div>
            <div className="ms-auto text-xs text-muted-foreground">
              {coverage.stats.records_total} records · {coverage.stats.vital} vital · {coverage.stats.frozen} frozen
            </div>
          </div>

          <div className="overflow-hidden rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
                <tr><th className="p-2 w-8" /><th className="p-2">Requirement</th><th className="p-2">SeDoc mechanism</th><th className="p-2">Detail</th></tr>
              </thead>
              <tbody>
                {coverage.requirements.map((req) => (
                  <tr key={req.id} className="border-t border-border align-top" data-testid={`req-${req.id}`}>
                    <td className="p-2"><StatusIcon status={req.status} /></td>
                    <td className="p-2">
                      <div className="font-medium">{req.title}</div>
                      <div className="text-xs text-muted-foreground">{req.id} — {req.description}</div>
                    </td>
                    <td className="p-2 text-xs">{req.mechanism}</td>
                    <td className={`p-2 text-xs ${req.status === 'fail' ? 'text-red-600' : 'text-muted-foreground'}`}>{req.detail}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="text-xs text-muted-foreground">
            Coverage = (pass + attested) / total. <span className="text-blue-600">Attested</span> = SeDoc provides the
            mechanism; <span className="text-emerald-600">pass</span> = live data confirms it; <span className="text-red-600">gap</span> = action needed.
            The evidence pack bundles this checklist with the audit hash-chain proof and the accession manifest.
          </p>
        </>
      ) : null}
    </div>
  )
}
