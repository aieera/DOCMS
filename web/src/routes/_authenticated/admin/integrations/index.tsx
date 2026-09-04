// /admin/integrations — CANONICAL unified Integrations page.
//
// Replaces the previous split between /admin/integrations-hub (which
// had the full top tab bar) and /admin/integrations (which rendered
// the same eSignature/notifications content without any way to reach
// the other sections). One URL, one source of truth.
//
// Six top-level tabs:
//   1. eSignature        — DocuSign / Adobe Sign OAuth, envelopes, notifications, connectors
//   2. Connectors        — third-party catalog (Salesforce / M365 / Google / etc)
//   3. Webhooks          — outbound webhook subscription management
//   4. Email ingestion   — IMAP/SMTP pollers
//   5. Event streaming   — per-tenant event mirroring
//   6. MCP               — LLM agent access keys
//
// Tab state is URL-driven via `?tab=`. Legacy URLs
// (/admin/integrations-hub, /admin/integrations/events,
// /admin/integrations/email, /admin/integrations/mcp) redirect into
// the matching tab on this page so existing deep-links keep working.
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState, useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Link2, Link2Off, Activity, AlertTriangle, CheckCircle2, RefreshCw } from 'lucide-react'

import {
  listESignConnections, startESignOAuth, disconnectESign, listESignEnvelopes,
  refreshESignConnection,
  type ESignProvider, type ESignConnection,
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
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { useAuthStore } from '@/store/authStore'
import { ConnectorsPage } from '../connectors'
import { WebhooksPage } from '../webhooks'
import { EmailIngestionPage } from './email'
import { EventStreamPage } from './events'
import { MCPPage } from './mcp'
import { IPaaSPage } from './ipaas'

type TopTab = 'esign' | 'connectors' | 'webhooks' | 'email' | 'events' | 'mcp' | 'ipaas'
const TOP_TABS: readonly TopTab[] = ['esign', 'connectors', 'webhooks', 'email', 'events', 'mcp', 'ipaas']
interface S { tab?: TopTab; esign_error?: string }

export const Route = createFileRoute('/_authenticated/admin/integrations/')({
  component: IntegrationsPage,
  validateSearch: (raw: Record<string, unknown>): S => {
    const t = raw.tab
    const s: S = TOP_TABS.includes(t as TopTab) ? { tab: t as TopTab } : {}
    // ADR 0071 — the eSign OAuth callback bounces back here with this
    // param when the connect fails (it's a top-level redirect, not an
    // XHR, so it can't toast directly).
    if (typeof raw.esign_error === 'string') s.esign_error = raw.esign_error
    return s
  },
})

function IntegrationsPage() {
  const navigate = useNavigate()
  const { tab, esign_error } = Route.useSearch()
  const active: TopTab = tab ?? 'esign'
  // Surface a failed eSign OAuth connect, then strip the param so a
  // refresh doesn't re-toast it.
  useEffect(() => {
    if (esign_error) {
      toast.error(`Couldn't connect e-signature provider: ${esign_error}`)
      navigate({ to: '/admin/integrations', search: { tab: 'esign' }, replace: true })
    }
  }, [esign_error, navigate])
  return (
    <div className="mx-auto w-full max-w-5xl p-6">
      <PageHeader
        title="Integrations"
        description="Third-party providers, outbound webhooks, inbound email + event streams, and MCP keys for this tenant."
      />
      <Tabs
        value={active}
        onValueChange={(v) => navigate({ to: '/admin/integrations', search: { tab: v as TopTab } })}
      >
        <TabsList>
          <TabsTrigger value="esign" data-testid="top-tab-esign">eSignature</TabsTrigger>
          <TabsTrigger value="connectors" data-testid="top-tab-connectors">Connectors</TabsTrigger>
          <TabsTrigger value="webhooks" data-testid="top-tab-webhooks">Webhooks</TabsTrigger>
          <TabsTrigger value="email" data-testid="top-tab-email">Email ingestion</TabsTrigger>
          <TabsTrigger value="events" data-testid="top-tab-events">Event streaming</TabsTrigger>
          <TabsTrigger value="mcp" data-testid="top-tab-mcp">MCP</TabsTrigger>
          <TabsTrigger value="ipaas" data-testid="top-tab-ipaas">iPaaS</TabsTrigger>
        </TabsList>
        <TabsContent value="esign" className="mt-4"><ESignatureSection /></TabsContent>
        <TabsContent value="connectors" className="mt-4"><ConnectorsPage /></TabsContent>
        <TabsContent value="webhooks" className="mt-4"><WebhooksPage /></TabsContent>
        <TabsContent value="email" className="mt-4"><EmailIngestionPage /></TabsContent>
        <TabsContent value="events" className="mt-4"><EventStreamPage /></TabsContent>
        <TabsContent value="mcp" className="mt-4"><MCPPage /></TabsContent>
        <TabsContent value="ipaas" className="mt-4"><IPaaSPage /></TabsContent>
      </Tabs>
    </div>
  )
}

// IntegrationsIndexPage is kept exported under its old name so the
// historical /admin/integrations-hub import path doesn't break while
// the redirect lands. New code should use the route directly.
export { IntegrationsPage as IntegrationsIndexPage }

// ----- eSignature section (formerly the entirety of /admin/integrations) -----

// ConnectionPill picks the colour + label off the server-derived
// status field. Falls back to the legacy 'Connected' (green) when
// the field is absent so older API responses still render.
function ConnectionPill({ conn, testId }: { conn: ESignConnection | undefined; testId: string }) {
  if (!conn) {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground" data-testid={testId}>
        Not connected
      </span>
    )
  }
  const status = conn.status ?? 'healthy'
  if (status === 'expired') {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-destructive/10 px-2 py-0.5 text-xs text-foreground" data-testid={testId}>
        <AlertTriangle className="h-3 w-3" /> Token expired
      </span>
    )
  }
  if (status === 'expiring_soon') {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-warning/15 px-2 py-0.5 text-xs text-warning-strong" data-testid={testId}>
        <AlertTriangle className="h-3 w-3" /> Token expiring soon
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs text-foreground" data-testid={testId}>
      <CheckCircle2 className="h-3 w-3" /> Connected
    </span>
  )
}

function ESignatureSection() {
  const qc = useQueryClient()
  // Inner sub-tab state inside the eSignature top-tab. Local state is
  // fine here — these are not deep-linked individually; the eSignature
  // top tab is the deep-linkable surface.
  const [tab, setTab] = useState<'connections' | 'envelopes' | 'notifications' | 'connectors'>('connections')
  const [credentialsFor, setCredentialsFor] = useState<ESignProvider | null>(null)
  const [twilioOpen, setTwilioOpen] = useState(false)
  const [smtpOpen, setSmtpOpen] = useState(false)
  const [googleOpen, setGoogleOpen] = useState(false)
  const twilioQ = useQuery({ queryKey: ['twilio-config'], queryFn: getTwilioConfig })
  const smtpQ = useQuery({ queryKey: ['smtp-config'], queryFn: getSMTPConfig })
  const googleQ = useQuery({ queryKey: ['google-connector'], queryFn: getGoogleConnector })
  const disconnectGoogleMut = useAppMutation({
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
  const refreshConn = useAppMutation({
    mutationFn: refreshESignConnection,
    onSuccess: () => {
      toast.success('Token refreshed')
      qc.invalidateQueries({ queryKey: ['esign-connections'] })
    },
    onError: (e: unknown) => {
      const msg = (e as { response?: { data?: { error?: string } } }).response?.data?.error
      toast.error(msg ?? 'Refresh failed — admin may need to reconnect')
    },
  })
  const disconnect = useAppMutation({
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
    <div>
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
          Workspace connectors
        </button>
      </div>

      {tab === 'connections' && (
        <section data-testid="connections-section">
          {connsQ.isLoading ? <Spinner /> : (
            <ul className="space-y-3">
              {PROVIDERS.map((p) => {
                const conn = connectionFor(p.id)
                return (
                  <li key={p.id} className="flex items-center justify-between rounded-lg bg-card p-4 shadow-neu" data-testid={`provider-row-${p.id}`}>
                    <div className="flex-1">
                      <div className="flex items-center gap-2">
                        <h3 className="text-base font-semibold">{p.label}</h3>
                        <ConnectionPill conn={conn} testId={`status-${p.id}`} />
                      </div>
                      <p className="mt-1 text-xs text-muted-foreground">{p.help}</p>
                      {conn?.account_id && (
                        <p className="mt-1 text-xs">Account: <span className="font-mono">{conn.account_id}</span></p>
                      )}
                      {conn && (
                        <p className="mt-0.5 text-xs text-muted-foreground">
                          Token {conn.status === 'expired' ? 'expired' : 'expires'} {new Date(conn.expires_at).toLocaleString()}
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
                      <div className="flex flex-col items-end gap-2">
                        {(conn.status === 'expiring_soon' || conn.status === 'expired') && (
                          <Button
                            size="sm"
                            onClick={() => refreshConn.mutate(p.id)}
                            loading={refreshConn.isPending && refreshConn.variables === p.id}
                            data-testid={`refresh-${p.id}`}
                          >
                            <RefreshCw className="me-1 h-4 w-4" />
                            {conn.status === 'expired' ? 'Reconnect / refresh' : 'Refresh token'}
                          </Button>
                        )}
                        <Button
                          size="sm" variant="outline"
                          onClick={() => disconnect.mutate(p.id)}
                          loading={disconnect.isPending && disconnect.variables === p.id}
                          data-testid={`disconnect-${p.id}`}
                        >
                          <Link2Off className="me-1 h-4 w-4" /> Disconnect
                        </Button>
                      </div>
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
            <div className="overflow-x-auto">
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
            </div>
          )}
        </section>
      )}

      {tab === 'notifications' && (
        <section data-testid="notifications-section" className="space-y-3">
          {/* Twilio card */}
          <div className="rounded-lg bg-card p-4 shadow-neu">
            <div className="flex items-center justify-between">
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <h3 className="text-base font-semibold">Twilio (SMS MFA)</h3>
                  {twilioQ.data ? (
                    <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs text-foreground">
                      <CheckCircle2 className="h-3 w-3" /> Configured
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
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
          <div className="rounded-lg bg-card p-4 shadow-neu">
            <div className="flex items-center justify-between">
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <h3 className="text-base font-semibold">SMTP (email)</h3>
                  {smtpQ.data ? (
                    <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs text-foreground">
                      <CheckCircle2 className="h-3 w-3" /> Configured
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
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
          <div className="rounded-lg bg-card p-4 shadow-neu">
            <div className="flex items-center justify-between">
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <h3 className="text-base font-semibold">Google Workspace</h3>
                  {googleQ.data ? (
                    <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs text-foreground">
                      <CheckCircle2 className="h-3 w-3" /> Connected
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
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
