import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Link2, Link2Off, Activity, CheckCircle2 } from 'lucide-react'

import {
  listESignConnections, startESignOAuth, disconnectESign, listESignEnvelopes,
  type ESignProvider,
} from '@/api/signatures'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Spinner } from '@/components/ui/Spinner'
import { useAuthStore } from '@/store/authStore'

// /admin/integrations — ADR 0071 OAuth + envelope-status surface.
//
// Two stacked sections: connections (Connect/Disconnect + token
// expiry) and the in-progress envelopes tab. Polls every 30s so a
// completed-via-webhook update lands without a hard refresh.

export const Route = createFileRoute('/_authenticated/admin/integrations')({
  component: IntegrationsPage,
})

function IntegrationsPage() {
  const qc = useQueryClient()
  const [tab, setTab] = useState<'connections' | 'envelopes'>('connections')
  const tenantID = useAuthStore((s) => s.tenantId)
  const origin = typeof window !== 'undefined' ? window.location.origin : ''
  const webhookURL = (provider: ESignProvider) =>
    `${origin}/api/v1/signatures/esign/webhook/${provider}/${tenantID ?? ''}`

  const connsQ = useQuery({ queryKey: ['esign-connections'], queryFn: listESignConnections, refetchInterval: 30_000 })
  const envsQ = useQuery({ queryKey: ['esign-envelopes'], queryFn: listESignEnvelopes, refetchInterval: 30_000 })

  const connect = useMutation({
    mutationFn: async (provider: ESignProvider) => {
      const url = await startESignOAuth(provider)
      window.location.href = url
    },
  })
  const disconnect = useMutation({
    mutationFn: disconnectESign,
    onSuccess: () => {
      toast.success('Disconnected')
      qc.invalidateQueries({ queryKey: ['esign-connections'] })
    },
  })

  const PROVIDERS: { id: ESignProvider; label: string; help: string }[] = [
    { id: 'docusign',   label: 'DocuSign',   help: 'eSignature REST API. OAuth2 authcode flow.' },
    { id: 'adobe_sign', label: 'Adobe Sign', help: 'Acrobat Sign REST API v6. OAuth2 authcode flow.' },
  ]

  const connectionFor = (id: ESignProvider) => connsQ.data?.find((c) => c.provider === id)

  return (
    <div className="mx-auto max-w-4xl p-6">
      <PageHeader
        title="Integrations"
        description="Connect third-party e-signature providers per blueprint §11.2 (ADR 0071)."
      />

      <div className="mb-4 flex gap-2 border-b border-border">
        <button
          onClick={() => setTab('connections')}
          className={`px-3 py-2 text-sm font-medium ${tab === 'connections' ? 'border-b-2 border-primary' : 'text-muted-foreground'}`}
          data-testid="tab-connections"
        >
          Connections
        </button>
        <button
          onClick={() => setTab('envelopes')}
          className={`px-3 py-2 text-sm font-medium ${tab === 'envelopes' ? 'border-b-2 border-primary' : 'text-muted-foreground'}`}
          data-testid="tab-envelopes"
        >
          In-progress envelopes
        </button>
      </div>

      {tab === 'connections' && (
        <section data-testid="connections-section">
          {connsQ.isLoading ? <Spinner /> : (
            <ul className="space-y-3">
              {PROVIDERS.map((p) => {
                const conn = connectionFor(p.id)
                return (
                  <li key={p.id} className="flex items-center justify-between rounded-lg border border-border bg-card p-4" data-testid={`provider-row-${p.id}`}>
                    <div className="flex-1">
                      <div className="flex items-center gap-2">
                        <h3 className="text-base font-semibold">{p.label}</h3>
                        {conn ? (
                          <span className="inline-flex items-center gap-1 rounded-full bg-emerald-100 px-2 py-0.5 text-xs text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200" data-testid={`status-${p.id}`}>
                            <CheckCircle2 className="h-3 w-3" /> Connected
                          </span>
                        ) : (
                          <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-slate-700 dark:bg-slate-700 dark:text-slate-200" data-testid={`status-${p.id}`}>
                            Not connected
                          </span>
                        )}
                      </div>
                      <p className="mt-1 text-xs text-muted-foreground">{p.help}</p>
                      {conn?.account_id && (
                        <p className="mt-1 text-xs">Account: <span className="font-mono">{conn.account_id}</span></p>
                      )}
                      {conn && (
                        <p className="mt-0.5 text-xs text-muted-foreground">
                          Token expires {new Date(conn.expires_at).toLocaleString()}
                        </p>
                      )}
                      {conn && (
                        <p className="mt-2 text-xs">
                          <span className="text-muted-foreground">Webhook URL: </span>
                          <code className="rounded bg-muted px-1 py-0.5 font-mono text-[10px] " data-testid={`webhook-url-${p.id}`}>
                            {webhookURL(p.id)}
                          </code>
                        </p>
                      )}
                    </div>
                    {conn ? (
                      <Button
                        size="sm" variant="outline"
                        onClick={() => disconnect.mutate(p.id)}
                        loading={disconnect.isPending && disconnect.variables === p.id}
                        data-testid={`disconnect-${p.id}`}
                      >
                        <Link2Off className="me-1 h-4 w-4" /> Disconnect
                      </Button>
                    ) : (
                      <Button
                        size="sm"
                        onClick={() => connect.mutate(p.id)}
                        loading={connect.isPending && connect.variables === p.id}
                        data-testid={`connect-${p.id}`}
                      >
                        <Link2 className="me-1 h-4 w-4" /> Connect
                      </Button>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </section>
      )}

      {tab === 'envelopes' && (
        <section data-testid="envelopes-section">
          {envsQ.isLoading ? <Spinner /> : (envsQ.data ?? []).length === 0 ? (
            <p className="text-sm text-muted-foreground">No envelopes in progress.</p>
          ) : (
            <table className="w-full text-sm" data-testid="envelopes-table">
              <thead>
                <tr className="border-b border-border text-start">
                  <th className="py-2 text-start font-medium">Request</th>
                  <th className="py-2 text-start font-medium">Envelope</th>
                  <th className="py-2 text-start font-medium">Provider</th>
                  <th className="py-2 text-start font-medium">Status</th>
                </tr>
              </thead>
              <tbody>
                {(envsQ.data ?? []).map((e) => (
                  <tr key={e.envelope_id} className="border-b border-border" data-testid={`envelope-row-${e.envelope_id}`}>
                    <td className="py-2 font-mono text-xs">{e.request_id.slice(0, 8)}…</td>
                    <td className="py-2 font-mono text-xs">{e.envelope_id}</td>
                    <td className="py-2">{e.provider}</td>
                    <td className="py-2">
                      <span className="inline-flex items-center gap-1 text-xs">
                        <Activity className="h-3 w-3" /> {e.status}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      )}
    </div>
  )
}
