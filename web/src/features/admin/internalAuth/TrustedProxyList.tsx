// TrustedProxyList — read-only display of the configured CIDRs + a
// dry-run tester. The list is echoed back by the server alongside the
// dry-run result so the UI is never out of sync with what the
// middleware actually sees.

import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import toast from 'react-hot-toast'
import { Play } from 'lucide-react'

import { testTrustedProxy, type TrustedProxyTestResult } from '@/api/platform'
import { internalAuthMessages as M } from '@/i18n/messages/internalAuth'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Badge } from '@/components/ui/Badge'

export function TrustedProxyList() {
  const [xff, setXff] = useState('198.51.100.9, 10.0.0.4')
  const [remote, setRemote] = useState('10.0.0.5:443')

  const run = useMutation({
    mutationFn: () =>
      testTrustedProxy({ x_forwarded_for: xff, remote_addr: remote }),
    onError: () => toast.error(M.healthError),
  })

  const cidrs = run.data?.trusted_cidrs ?? []

  return (
    <section
      aria-labelledby="trusted-proxy-title"
      data-testid="trusted-proxy-panel"
      className="space-y-4"
    >
      <header>
        <h2 id="trusted-proxy-title" className="text-sm font-semibold">
          {M.trustedProxyTitle}
        </h2>
        <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
          {M.trustedProxyDescription}
        </p>
      </header>

      <div
        className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
        data-testid="trusted-proxy-cidrs"
      >
        {cidrs.length === 0 && !run.isPending ? (
          <p className="text-sm text-[var(--color-text-secondary)]">
            {M.trustedProxyEmpty}
            <span className="ml-2 italic opacity-60">
              (Run the tester below to fetch the current list.)
            </span>
          </p>
        ) : (
          <ul className="flex flex-wrap gap-2" aria-label="Trusted CIDRs">
            {cidrs.map((c) => (
              <li key={c}>
                <Badge variant="outline" className="font-mono">
                  {c}
                </Badge>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="space-y-3 rounded-xl border border-[var(--color-border)] p-4">
        <div>
          <h3 className="text-sm font-semibold">{M.dryRunTitle}</h3>
          <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
            {M.dryRunDescription}
          </p>
        </div>
        <form
          className="grid gap-3 sm:grid-cols-[1fr_200px_auto]"
          onSubmit={(e) => {
            e.preventDefault()
            run.mutate()
          }}
        >
          <Input
            label={M.dryRunXff}
            value={xff}
            onChange={(e) => setXff(e.target.value)}
            data-testid="trusted-proxy-xff"
          />
          <Input
            label={M.dryRunRemoteAddr}
            value={remote}
            onChange={(e) => setRemote(e.target.value)}
            data-testid="trusted-proxy-remote"
          />
          <div className="flex items-end">
            <Button
              type="submit"
              loading={run.isPending}
              data-testid="trusted-proxy-run"
            >
              <Play className="mr-1 h-4 w-4" aria-hidden="true" />
              {M.dryRunRun}
            </Button>
          </div>
        </form>

        {run.data ? <DryRunResult result={run.data} /> : null}
      </div>
    </section>
  )
}

function DryRunResult({ result }: { result: TrustedProxyTestResult }) {
  return (
    <div
      className="mt-2 space-y-2 rounded-md bg-[var(--color-bg-secondary)] p-3 text-sm"
      role="status"
      aria-live="polite"
      data-testid="trusted-proxy-result"
    >
      <div>
        <span className="text-xs text-[var(--color-text-secondary)]">
          {M.dryRunResolvedIP}:
        </span>{' '}
        <span className="font-mono font-semibold" data-testid="trusted-proxy-resolved">
          {result.resolved_ip || '—'}
        </span>
      </div>

      <HopRow label={M.dryRunPeerLabel} hop={result.peer} />

      {result.hops.length > 0 ? (
        <div>
          <div className="mb-1 text-xs text-[var(--color-text-secondary)]">
            {M.dryRunHopsLabel}
          </div>
          <ol className="space-y-1">
            {[...result.hops].reverse().map((h, i) => (
              <HopRow key={`${h.addr}-${i}`} hop={h} />
            ))}
          </ol>
        </div>
      ) : null}
    </div>
  )
}

function HopRow({
  label,
  hop,
}: {
  label?: string
  hop: TrustedProxyTestResult['hops'][number]
}) {
  const tag = !hop.parseable
    ? M.dryRunHopUnparseable
    : hop.trusted
      ? M.dryRunHopTrusted
      : M.dryRunHopUntrusted
  const tone = !hop.parseable
    ? 'bg-gray-200 text-gray-800 dark:bg-gray-800 dark:text-gray-200'
    : hop.trusted
      ? 'bg-green-100 text-green-900 dark:bg-green-900/40 dark:text-green-100'
      : 'bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-100'
  return (
    <li className="flex items-center gap-2 text-sm">
      {label ? (
        <span className="w-12 text-xs text-[var(--color-text-secondary)]">
          {label}
        </span>
      ) : null}
      <code className="font-mono">{hop.addr || '(empty)'}</code>
      <span
        className={`rounded-md px-2 py-0.5 text-xs font-medium ${tone}`}
        data-testid={`hop-tag-${hop.addr || 'empty'}`}
      >
        {tag}
      </span>
    </li>
  )
}
