// /admin/integrations — ADR 0071 OAuth + envelope-status surface +
// per-tenant notification provider configuration.
//
// Three tabs:
//   1. Connections — DocuSign / Adobe Sign OAuth (Connect/Disconnect, token expiry)
//   2. In-progress envelopes — live list of vendor envelopes; polled
//      every 30s so a completed-via-webhook update lands without refresh
//   3. Notifications — per-tenant Twilio (SMS MFA) + SMTP credentials
//
// This file is the INDEX child of the integrations layout. Sibling
// child routes (events.tsx, email.tsx) are mounted at
// /admin/integrations/events and /admin/integrations/email and render
// into the same parent <Outlet />. Keeping this as a sibling instead
// of inlining it into integrations.tsx is what stops the parent's
// content from rendering on /events (the bug it replaces).
import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Link2, Link2Off, Activity, CheckCircle2 } from 'lucide-react'

import {
  listESignConnections, startESignOAuth, disconnectESign, listESignEnvelopes,
  type ESignProvider,
} from '@/api/signatures'
import { getTwilioConfig, getSMTPConfig } from '@/api/notif-providers'
import { getGoogleConnector, disconnectGoogle } from '@/api/connectors'
import { ESignCredentialsModal } from '@/components/admin/ESignCredentialsModal'
import { TwilioCredentialsModal } from '@/components/admin/TwilioCredentialsModal'
import { SMTPCredentialsModal } from '@/components/admin/SMTPCredentialsModal'
import { GoogleWorkspaceModal } from '@/components/admin/GoogleWorkspaceModal'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { useAuthStore } from '@/store/authStore'

export const Route = createFileRoute('/_authenticated/admin/integrations/')({
  component: IntegrationsIndexPage,
})

export function IntegrationsIndexPage() {
  const qc = useQueryClient()
  const [tab, setTab] = useState<'connections' | 'envelopes' | 'notifications' | 'connectors'>('connections')
  const [credentialsFor, setCredentialsFor] = useState<ESignProvider | null>(null)
  const [twilioOpen, setTwilioOpen] = useState(false)
  const [smtpOpen, setSmtpOpen] = useState(false)
  const [googleOpen, setGoogleOpen] = useState(false)
  const twilioQ = useQuery({ queryKey: ['twilio-config'], queryFn: getTwilioConfig })
  const smtpQ = useQuery({ queryKey: ['smtp-config'], queryFn: getSMTPConfig })
  const googleQ = useQuery({ queryKey: ['google-connector'], queryFn: getGoogleConnector })
  const disconnectGoogleMut = useMutation({
    mutationFn: disconnectGoogle,
    onSuccess: () => {
      toast.success('Google disconnected')
      qc.invalidateQueries({ queryKey: ['google-connector'] })
    },
  })
  const tenantID = useAuthStore((s) => s.tenantId)
  const origin = typeof window !== 'undefined' ? window.location.origin : ''
  const webhookURL = (provider: ESignProvider) =>
    `${origin}/api/v1/signatures/esign/webhook/${provider}/${tenantID ?? ''}`
  const redirectURI = `${origin}/api/v1/signatures/esign/oauth/callback`

  const connsQ = useQuery({ queryKey: ['esign-connections'], queryFn: listESignConnections, refetchInterval: 30_000 })
  const envsQ = useQuery({ queryKey: ['esign-envelopes'], queryFn: listESignEnvelopes, refetchInterval: 30_000 })

  const launchOAuth = async (provider: ESignProvider) => {
    try {
      const url = await startESignOAuth(provider)
      window.location.href = url
    } catch (e) {
      const detail = (e as { response?: { data?: { error?: string } } }).response?.data?.error
      toast.error(detail || 'Could not start OAuth — check credentials')
    }
  }
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
        description="Connect third-party providers for this tenant: e-signature (DocuSign / Adobe Sign), SMS MFA (Twilio), and outbound email (SMTP)."
      />

      <div className="mb-4 flex flex-wrap gap-2 text-xs">
        <Link to="/admin/integrations/events" className="rounded-full border border-border bg-card px-3 py-1 hover:bg-muted">
          Event streaming →
        </Link>
        <Link to="/admin/integrations/email" className="rounded-full border border-border bg-card px-3 py-1 hover:bg-muted">
          Email ingestion →
        </Link>
        <Link to="/admin/integrations/mcp" className="rounded-full border border-border bg-card px-3 py-1 hover:bg-muted">
          MCP (LLM agents) →
        </Link>
        {/* iPaaS chip hidden (ADR 0090) — the route + backend triggers
            still work; we just don't surface them until the external
            Zapier/Make/n8n apps are published.
        <Link to="/admin/integrations/ipaas" className="rounded-full border border-border bg-card px-3 py-1 hover:bg-muted">
          iPaaS (Zapier / Make / n8n) →
        </Link>
        */}
      </div>

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
        <button
          onClick={() => setTab('notifications')}
          className={`px-3 py-2 text-sm font-medium ${tab === 'notifications' ? 'border-b-2 border-primary' : 'text-muted-foreground'}`}
          data-testid="tab-notifications"
        >
          Notifications
        </button>
        <button
          onClick={() => setTab('connectors')}
          className={`px-3 py-2 text-sm font-medium ${tab === 'connectors' ? 'border-b-2 border-primary' : 'text-muted-foreground'}`}
          data-testid="tab-connectors"
        >
          Connectors
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
                        onClick={() => setCredentialsFor(p.id)}
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

      {tab === 'notifications' && (
        <section data-testid="notifications-section" className="space-y-3">
          {/* Twilio card */}
          <div className="rounded-lg border border-border bg-card p-4">
            <div className="flex items-center justify-between">
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <h3 className="text-base font-semibold">Twilio (SMS MFA)</h3>
                  {twilioQ.data ? (
                    <span className="inline-flex items-center gap-1 rounded-full bg-emerald-100 px-2 py-0.5 text-xs text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200">
                      <CheckCircle2 className="h-3 w-3" /> Configured
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-slate-700 dark:bg-slate-700 dark:text-slate-200">
                      Not configured
                    </span>
                  )}
                </div>
                <p className="mt-1 text-xs text-muted-foreground">
                  Per-tenant Twilio Verify account for SMS-based MFA. Without this, login falls back to the deployment-wide env-mode account (if any).
                </p>
                {twilioQ.data && (
                  <p className="mt-1 text-xs">
                    Account: <span className="font-mono">{twilioQ.data.account_sid}</span>
                  </p>
                )}
              </div>
              <Button size="sm" onClick={() => setTwilioOpen(true)} data-testid="configure-twilio">
                {twilioQ.data ? 'Edit credentials' : 'Configure'}
              </Button>
            </div>
          </div>

          {/* SMTP card */}
          <div className="rounded-lg border border-border bg-card p-4">
            <div className="flex items-center justify-between">
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <h3 className="text-base font-semibold">SMTP (email)</h3>
                  {smtpQ.data ? (
                    <span className="inline-flex items-center gap-1 rounded-full bg-emerald-100 px-2 py-0.5 text-xs text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200">
                      <CheckCircle2 className="h-3 w-3" /> Configured
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-slate-700 dark:bg-slate-700 dark:text-slate-200">
                      Not configured
                    </span>
                  )}
                </div>
                <p className="mt-1 text-xs text-muted-foreground">
                  Per-tenant outbound mail relay. Used for email MFA OTP, signature invites, share-link emails, password reset, and notification digests.
                </p>
                {smtpQ.data && (
                  <p className="mt-1 text-xs">
                    <span className="font-mono">{smtpQ.data.host}:{smtpQ.data.port}</span>
                    {' '}as <span className="font-mono">{smtpQ.data.from_addr}</span>
                  </p>
                )}
              </div>
              <Button size="sm" onClick={() => setSmtpOpen(true)} data-testid="configure-smtp">
                {smtpQ.data ? 'Edit credentials' : 'Configure'}
              </Button>
            </div>
          </div>
        </section>
      )}

      {tab === 'connectors' && (
        <section data-testid="connectors-section" className="space-y-3">
          {/* Google Workspace */}
          <div className="rounded-lg border border-border bg-card p-4">
            <div className="flex items-center justify-between">
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <h3 className="text-base font-semibold">Google Workspace</h3>
                  {googleQ.data ? (
                    <span className="inline-flex items-center gap-1 rounded-full bg-emerald-100 px-2 py-0.5 text-xs text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200">
                      <CheckCircle2 className="h-3 w-3" /> Connected
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-slate-700 dark:bg-slate-700 dark:text-slate-200">
                      Not configured
                    </span>
                  )}
                </div>
                <p className="mt-1 text-xs text-muted-foreground">
                  Pull documents from Drive and Gmail attachments per tenant. Drive/Gmail import lands in a follow-up; OAuth handshake works today.
                </p>
                {googleQ.data?.last_sync_at && (
                  <p className="mt-1 text-xs">
                    Last sync: <span className="font-mono">{new Date(googleQ.data.last_sync_at).toLocaleString()}</span>
                  </p>
                )}
              </div>
              {googleQ.data ? (
                <Button
                  size="sm" variant="outline"
                  onClick={() => disconnectGoogleMut.mutate()}
                  loading={disconnectGoogleMut.isPending}
                  data-testid="disconnect-google"
                >
                  <Link2Off className="me-1 h-4 w-4" /> Disconnect
                </Button>
              ) : (
                <Button size="sm" onClick={() => setGoogleOpen(true)} data-testid="configure-google">
                  <Link2 className="me-1 h-4 w-4" /> Connect
                </Button>
              )}
            </div>
          </div>

          {/* Placeholder cards for the rest of §12.4 — show the roadmap so admins know what's coming. */}
          {(['Salesforce', 'Microsoft 365', 'ServiceNow', 'Workday', 'SAP ArchiveLink', 'QuickBooks Online', 'NetSuite', 'Xero'] as const).map((label) => (
            <div key={label} className="rounded-lg border border-dashed border-border bg-card/40 p-4 opacity-60">
              <div className="flex items-center justify-between">
                <div className="flex-1">
                  <h3 className="text-base font-semibold">{label}</h3>
                  <p className="mt-1 text-xs text-muted-foreground">Coming soon — scaffolded, not yet wired.</p>
                </div>
                <Button size="sm" disabled>Configure</Button>
              </div>
            </div>
          ))}
        </section>
      )}

      {credentialsFor && (
        <ESignCredentialsModal
          open={!!credentialsFor}
          onOpenChange={(open) => { if (!open) setCredentialsFor(null) }}
          provider={credentialsFor}
          redirectURI={redirectURI}
          webhookURL={webhookURL(credentialsFor)}
          onSaved={async () => {
            const p = credentialsFor
            setCredentialsFor(null)
            qc.invalidateQueries({ queryKey: ['esign-connections'] })
            if (p) await launchOAuth(p)
          }}
        />
      )}

      <TwilioCredentialsModal
        open={twilioOpen}
        onOpenChange={setTwilioOpen}
        onSaved={async () => { setTwilioOpen(false) }}
      />
      <SMTPCredentialsModal
        open={smtpOpen}
        onOpenChange={setSmtpOpen}
        onSaved={async () => { setSmtpOpen(false) }}
      />
      <GoogleWorkspaceModal
        open={googleOpen}
        onOpenChange={setGoogleOpen}
        redirectURI={`${origin}/api/v1/connectors/oauth/callback`}
        onSaved={async () => { setGoogleOpen(false) }}
      />
    </div>
  )
}
