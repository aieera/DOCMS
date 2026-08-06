import { copyText } from '@/lib/clipboard'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Trash2, Copy, Check, Plus, ExternalLink, Zap } from 'lucide-react'

import { listAPIKeys, createAPIKey, revokeAPIKey } from '@/api/security'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
  DialogDescription, DialogFooter,
} from '@/components/ui/shadcn/dialog'

// /admin/integrations/ipaas — Zapier / Make / n8n connection surface (ADR 0090).
//
// Three concerns on this page:
//   1. API keys with scope (UI for issuing + revoking keys the
//      iPaaS apps will Bearer-auth with).
//   2. Trigger-endpoint reference card showing the polling URLs.
//   3. Getting-started links pointing at the actual Zapier / Make /
//      n8n developer portals where the user builds their apps.

// Standalone URL deep-links into the tabbed Integrations shell; the
// page component below is what the shell renders as its iPaaS tab.
export const Route = createFileRoute('/_authenticated/admin/integrations/ipaas')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/integrations', search: { tab: 'ipaas' }, replace: true })
  },
})

const AVAILABLE_SCOPES = [
  { id: 'integrations:read', label: 'integrations:read', desc: 'Poll trigger endpoints (documents, signatures, workflows)' },
  { id: 'integrations:write', label: 'integrations:write', desc: 'Future: perform actions (upload, start workflow) from iPaaS' },
  { id: 'integrations:*', label: 'integrations:*', desc: 'Both of the above' },
]

export function IPaaSPage() {
  const qc = useQueryClient()
  const keysQ = useQuery({ queryKey: ['api-keys'], queryFn: listAPIKeys })
  const [createOpen, setCreateOpen] = useState(false)
  const [revealedKey, setRevealedKey] = useState<{ id: string; plaintext: string } | null>(null)

  const revoke = useAppMutation({
    mutationFn: revokeAPIKey,
    onSuccess: () => {
      toast.success('API key revoked')
      qc.invalidateQueries({ queryKey: ['api-keys'] })
    },
  })

  const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost:3000'

  return (
    <div className="space-y-6">
      <PageHeader
        title="iPaaS integrations"
        description="Connect SeDoc to Zapier, Make, n8n and other automation platforms. Issue API keys with scoped access, then build your Zap / scenario / workflow in the vendor's developer portal."
      />

      {/* ---- API keys section ------------------------------------------------ */}
      <section className="mb-8" data-testid="api-keys-section">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-lg font-semibold">API keys</h2>
          <Button size="sm" onClick={() => setCreateOpen(true)} data-testid="create-key">
            <Plus className="me-1 h-4 w-4" /> New API key
          </Button>
        </div>

        {keysQ.isLoading ? <Spinner /> : (keysQ.data ?? []).length === 0 ? (
          <div className="rounded-lg border border-dashed border-border bg-card/40 p-6 text-center text-sm text-muted-foreground">
            No API keys yet. Create one to connect Zapier, Make, n8n, or any HTTP-capable automation tool.
          </div>
        ) : (
          <ul className="space-y-2">
            {(keysQ.data ?? []).map((k) => (
              <li
                key={k.key_id}
                className="flex items-center justify-between rounded-lg border border-border bg-card p-4"
                data-testid={`api-key-row-${k.key_id}`}
              >
                <div className="flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{k.name}</span>
                    <code className="rounded bg-muted px-2 py-0.5 font-mono text-xs">{k.key_prefix}…</code>
                  </div>
                  <div className="mt-1 flex flex-wrap gap-1 text-xs">
                    {(k.scopes ?? []).map((s) => (
                      <span key={s} className="rounded-full bg-muted px-2 py-0.5 font-mono">{s}</span>
                    ))}
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Created {new Date(k.created_at).toLocaleString()}
                    {k.last_used_at && ` · last used ${new Date(k.last_used_at).toLocaleString()}`}
                    {k.expires_at && ` · expires ${new Date(k.expires_at).toLocaleString()}`}
                  </p>
                </div>
                <Button
                  size="sm" variant="outline"
                  onClick={() => {
                    if (confirm(`Revoke API key "${k.name}"? Any iPaaS Zap or Make scenario using it will stop working.`)) {
                      revoke.mutate(k.key_id)
                    }
                  }}
                  data-testid={`revoke-key-${k.key_id}`}
                >
                  <Trash2 className="me-1 h-4 w-4" /> Revoke
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* ---- Trigger endpoints reference ------------------------------------ */}
      <section className="mb-8">
        <h2 className="mb-3 text-lg font-semibold">Trigger endpoints</h2>
        <p className="mb-3 text-sm text-muted-foreground">
          Configure these as polling URLs in your Zap / Make scenario. Authenticate with{' '}
          <code className="rounded bg-muted px-1 text-xs">Authorization: Bearer vdms_…</code>{' '}
          and a key with the <code className="rounded bg-muted px-1 text-xs">integrations:read</code> scope.
          Each endpoint accepts <code className="rounded bg-muted px-1 text-xs">?since=&lt;RFC3339&gt;&amp;limit=&lt;1-100&gt;</code>.
        </p>
        <div className="space-y-2">
          {[
            { trigger: 'document.created', path: '/api/v1/integrations/triggers/documents' },
            { trigger: 'signature.completed', path: '/api/v1/integrations/triggers/signatures/completed' },
            { trigger: 'workflow.completed', path: '/api/v1/integrations/triggers/workflows/completed' },
          ].map((row) => (
            <CopyableEndpoint
              key={row.trigger}
              trigger={row.trigger}
              url={`${origin}${row.path}`}
            />
          ))}
        </div>
      </section>

      {/* ---- Getting started cards ----------------------------------------- */}
      <section>
        <h2 className="mb-3 text-lg font-semibold">Getting started</h2>
        <p className="mb-3 text-sm text-muted-foreground">
          Build your app on the vendor's developer portal — SeDoc provides the HTTP surface; the app definition lives on their side.
        </p>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
          <PortalCard
            name="Zapier"
            description="Build a private or public Zapier app. Configure each trigger as a Polling URL pointing at the endpoints above."
            url="https://zapier.com/developer/builder"
          />
          <PortalCard
            name="Make"
            description="Make.com's Apps Developer panel. Define modules calling the same endpoints."
            url="https://www.make.com/en/help/apps/about-the-app-development-platform"
          />
          <PortalCard
            name="n8n"
            description="Build a community node and publish to npm as n8n-nodes-vaultdms. Lives in a separate repo."
            url="https://docs.n8n.io/integrations/creating-nodes/build/declarative-style-node/"
          />
        </div>
      </section>

      <CreateKeyDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(plaintext, id) => {
          setRevealedKey({ id, plaintext })
          qc.invalidateQueries({ queryKey: ['api-keys'] })
        }}
      />

      {revealedKey && (
        <RevealedKeyDialog
          open={!!revealedKey}
          plaintext={revealedKey.plaintext}
          onClose={() => setRevealedKey(null)}
        />
      )}
    </div>
  )
}

// ---- Subcomponents ---------------------------------------------------------

function CopyableEndpoint({ trigger, url }: { trigger: string; url: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="rounded-lg border border-border bg-card p-3">
      <div className="flex items-center justify-between gap-2">
        <div className="flex-1">
          <div className="flex items-center gap-2 text-sm font-medium">
            <Zap className="h-3.5 w-3.5 text-amber-500" /> {trigger}
          </div>
          <code className="mt-1 block truncate text-xs text-muted-foreground">{url}</code>
        </div>
        <Button
          size="sm" variant="ghost"
          onClick={() => copyText(url).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) })}
          aria-label="Copy URL"
        >
          {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
        </Button>
      </div>
    </div>
  )
}

function PortalCard({ name, description, url }: { name: string; description: string; url: string }) {
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className="block rounded-lg border border-border bg-card p-4 transition-colors hover:border-primary"
    >
      <div className="flex items-center gap-2">
        <span className="font-semibold">{name}</span>
        <ExternalLink className="h-3.5 w-3.5 text-muted-foreground" />
      </div>
      <p className="mt-1 text-xs text-muted-foreground">{description}</p>
    </a>
  )
}

function CreateKeyDialog({ open, onOpenChange, onCreated }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (plaintext: string, id: string) => void
}) {
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<string[]>(['integrations:read'])
  const [expiresInDays, setExpiresInDays] = useState<number | ''>('')

  const create = useAppMutation({
    mutationFn: () => createAPIKey({
      name: name.trim(),
      scopes,
      expires_in_days: expiresInDays === '' ? undefined : Number(expiresInDays),
    }),
    onSuccess: (data) => {
      onCreated(data.api_key, data.key_id)
      onOpenChange(false)
      setName('')
      setScopes(['integrations:read'])
      setExpiresInDays('')
    },
    onError: (e: Error) => toast.error(e.message || 'Failed to create key'),
  })

  const toggleScope = (id: string) => {
    setScopes((s) => s.includes(id) ? s.filter((x) => x !== id) : [...s, id])
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>New API key</DialogTitle>
          <DialogDescription>
            The key value is shown ONCE after creation — copy it immediately. Re-creating is the only way to recover from a lost key.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div>
            <label className="mb-1 block text-sm font-medium">Name</label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Zapier production"
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">Scopes</label>
            <div className="space-y-1">
              {AVAILABLE_SCOPES.map((s) => (
                <label key={s.id} className="flex cursor-pointer items-start gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={scopes.includes(s.id)}
                    onChange={() => toggleScope(s.id)}
                    className="mt-0.5"
                  />
                  <div>
                    <code className="font-mono text-xs">{s.label}</code>
                    <div className="text-xs text-muted-foreground">{s.desc}</div>
                  </div>
                </label>
              ))}
            </div>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              Expires in <span className="text-xs text-muted-foreground">(days, blank = never)</span>
            </label>
            <Input
              type="number"
              value={expiresInDays}
              onChange={(e) => setExpiresInDays(e.target.value === '' ? '' : Number(e.target.value))}
              placeholder="365"
              min={1}
            />
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button
            onClick={() => create.mutate()}
            disabled={name.trim() === '' || scopes.length === 0 || create.isPending}
          >
            {create.isPending ? 'Creating…' : 'Create key'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function RevealedKeyDialog({ open, plaintext, onClose }: {
  open: boolean
  plaintext: string
  onClose: () => void
}) {
  const [copied, setCopied] = useState(false)
  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) onClose() }}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Copy your API key now</DialogTitle>
          <DialogDescription>
            This is the only time we'll show this value. Paste it into your Zapier / Make / n8n app's authentication settings.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3 py-2">
          <div className="flex items-center gap-2">
            <code className="flex-1 truncate rounded border border-amber-500/40 bg-amber-50 px-3 py-2 font-mono text-xs dark:bg-amber-950/30">
              {plaintext}
            </code>
            <Button
              size="sm"
              onClick={() => copyText(plaintext).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) })}
            >
              {copied ? <Check className="me-1 h-4 w-4" /> : <Copy className="me-1 h-4 w-4" />}
              {copied ? 'Copied' : 'Copy'}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            Authorization header format: <code className="rounded bg-muted px-1 font-mono">Bearer {plaintext.slice(0, 20)}…</code>
          </p>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>I've saved the key</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
