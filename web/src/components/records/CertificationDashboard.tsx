import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
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
  const { t } = useTranslation('common')
  // The icon was the ONLY signal for pass/fail/attested — no text at all,
  // so a screen reader announced nothing. Label it from the catalogue.
  if (status === 'pass') return <CheckCircle2 className="h-4 w-4 text-emerald-500" role="img" aria-label={t('adm.cert.st_pass')} />
  if (status === 'fail') return <XCircle className="h-4 w-4 text-red-500" role="img" aria-label={t('adm.cert.st_fail')} />
  return <ShieldQuestion className="h-4 w-4 text-blue-500" role="img" aria-label={t('adm.cert.st_attested')} />
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
  const { t } = useTranslation('common')
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
        verifyAuditIntegrity().catch(() => ({ error: t('adm.cert.audit_unavailable') })),
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
      toast.success(t('adm.cert.exported'))
    } catch (e) {
      toast.error(t('adm.cert.err_export', { error: String(e) }))
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className="space-y-4" data-testid="certification-dashboard">
      <div className="flex flex-wrap items-center gap-3">
        <label className="text-sm font-medium">{t('adm.cert.standard')}</label>
        <select
          className="rounded border border-border bg-background px-2 py-1 text-sm"
          value={standardId}
          onChange={(e) => setStandardId(e.target.value)}
          data-testid="standard-selector"
        >
          {standards.map((s) => (
            <option key={s.id} value={s.id}>{t('adm.cert.standard_option', { name: s.name, count: s.requirements })}</option>
          ))}
        </select>
        <Button size="sm" onClick={exportEvidence} disabled={exporting} className="ms-auto" data-testid="export-evidence">
          {exporting ? <Spinner className="h-3 w-3" /> : <Download className="h-3 w-3" />}
          {t('adm.cert.export')}
        </Button>
      </div>

      {isLoading ? (
        <div className="flex items-center gap-2 p-4 text-sm text-muted-foreground"><Spinner className="h-4 w-4" /> {t('adm.cert.evaluating')}</div>
      ) : coverage ? (
        <>
          <div className="flex flex-wrap items-center gap-4 rounded-lg border border-border p-4">
            <div className="flex items-center gap-2">
              <FileBadge className="h-8 w-8 text-violet-500" />
              <div>
                <div className="text-2xl font-bold tabular-nums">{coverage.coverage_pct}%</div>
                <div className="text-xs text-muted-foreground">{t('adm.cert.coverage')}</div>
              </div>
            </div>
            <div className="flex gap-4 text-sm">
              <span className="text-emerald-600">{t('adm.cert.passed', { count: coverage.passed })}</span>
              <span className="text-blue-600">{t('adm.cert.attested', { count: coverage.attested })}</span>
              <span className={coverage.gaps > 0 ? 'text-red-600' : 'text-muted-foreground'}>{t('adm.cert.gaps', { count: coverage.gaps })}</span>
            </div>
            <div dir="auto" className="ms-auto text-xs text-muted-foreground">
              {t('adm.cert.stats', { records: coverage.stats.records_total, vital: coverage.stats.vital, frozen: coverage.stats.frozen })}
            </div>
          </div>

          <div className="overflow-hidden rounded-lg border border-border">
            <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-start text-xs text-muted-foreground">
                <tr><th className="p-2 w-8" /><th className="p-2">{t('adm.cert.col_requirement')}</th><th className="p-2">{t('adm.cert.col_mechanism')}</th><th className="p-2">{t('adm.cert.col_detail')}</th></tr>
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
          </div>
          <p className="text-xs text-muted-foreground">
            {/* Three colour-coded terms sit mid-sentence; Arabic reorders the
                clauses around them, so the markup travels with the key. */}
            <Trans
              i18nKey="adm.cert.legend"
              ns="common"
              components={[
                <span key="a" className="text-blue-600" />,
                <span key="p" className="text-emerald-600" />,
                <span key="g" className="text-red-600" />,
              ]}
            />
          </p>
        </>
      ) : null}
    </div>
  )
}
