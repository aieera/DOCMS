import { copyText } from '@/lib/clipboard'
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Trash2, Copy, Check, Plus, Bot } from 'lucide-react'

import { listAPIKeys, createAPIKey, revokeAPIKey } from '@/api/security'
import { listMCPTools } from '@/api/mcp'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
  DialogDescription, DialogFooter,
} from '@/components/ui/shadcn/dialog'

// /admin/integrations/mcp — Model Context Protocol surface for LLM
// agents (ADR 0091). Two concerns:
//   1. API-key issuance with mcp:read / mcp:write scopes
//   2. Copy-paste install snippets for Claude Desktop, Cursor, Copilot

// The route is a redirect alias into the canonical
// /admin/integrations?tab=mcp. MCPPage is exported below so the
// canonical page can render it inside its 'mcp' tab.
import { redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_authenticated/admin/integrations/mcp')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/integrations', search: { tab: 'mcp' }, replace: true })
  },
})

const MCP_SCOPES = [
  { id: 'mcp:read',  label: 'mcp:read',  desc: 'Search documents, get document metadata, list workflows' },
  { id: 'mcp:write', label: 'mcp:write', desc: 'Upload documents, start workflows' },
  { id: 'mcp:*',     label: 'mcp:*',     desc: 'Both read and write' },
]

export function MCPPage() {
  const qc = useQueryClient()
  const keysQ = useQuery({ queryKey: ['api-keys'], queryFn: listAPIKeys })
  const toolsQ = useQuery({ queryKey: ['mcp-tools'], queryFn: listMCPTools, refetchInterval: 60_000 })
  const [createOpen, setCreateOpen] = useState(false)
  const [revealedKey, setRevealedKey] = useState<{ id: string; plaintext: string } | null>(null)

  const revoke = useAppMutation({
    mutationFn: revokeAPIKey,
    onSuccess: () => {
      toast.success('API key revoked — any LLM agent using it will fail next call')
      qc.invalidateQueries({ queryKey: ['api-keys'] })
    },
  })

  const mcpKeys = (keysQ.data ?? []).filter((k) =>
    (k.scopes ?? []).some((s) => s.startsWith('mcp:')),
  )

  const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost:3000'
  const mcpURL = `${origin}/api/v1/mcp`

  return (
    <div className="space-y-6">
      <PageHeader
        title="MCP server (LLM agents)"
        description="Let Claude Desktop, Cursor, GitHub Copilot, and other Model Context Protocol clients call SeDoc tools (search, get, upload, start workflow). Issue an API key with mcp scopes, then paste the matching snippet into your client's config."
      />

      {/* ---- Server status -------------------------------------------- */}
      <section className="mb-6">
        <h2 className="mb-2 text-lg font-semibold">Server</h2>
        <div className="flex items-center justify-between rounded-lg border border-border bg-card p-4">
          <div className="flex-1">
            <div className="flex items-center gap-2">
              <Bot className="h-4 w-4" />
              <span className="font-medium">vaultdms-mcp</span>
              {toolsQ.isLoading ? (
                <span className="text-xs text-muted-foreground">checking…</span>
              ) : (toolsQ.data?.length ?? 0) > 0 ? (
                <span className="inline-flex items-center gap-1 rounded-full bg-emerald-100 px-2 py-0.5 text-xs text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200">
                  Reachable · {toolsQ.data?.length} tools
                </span>
              ) : (
                <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs">
                  Unreachable or unauthenticated
                </span>
              )}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              Endpoint: <code className="rounded bg-muted px-1 font-mono">{mcpURL}</code>
            </p>
          </div>
        </div>
        {(toolsQ.data?.length ?? 0) > 0 && (
          <ul className="mt-2 grid grid-cols-1 gap-2 sm:grid-cols-2">
            {toolsQ.data!.map((t) => (
              <li key={t.name} className="rounded border border-border bg-card/40 p-2 text-xs">
                <code className="font-mono font-semibold">{t.name}</code>
                <p className="mt-0.5 text-muted-foreground">{t.description}</p>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* ---- API keys ------------------------------------------------- */}
      <section className="mb-8" data-testid="mcp-api-keys-section">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-lg font-semibold">MCP API keys</h2>
          <Button size="sm" onClick={() => setCreateOpen(true)} data-testid="create-mcp-key">
            <Plus className="me-1 h-4 w-4" /> New MCP key
          </Button>
        </div>

        {keysQ.isLoading ? <Spinner /> : mcpKeys.length === 0 ? (
          <div className="rounded-lg border border-dashed border-border bg-card/40 p-6 text-center text-sm text-muted-foreground">
            No MCP keys yet. Create one with <code className="rounded bg-muted px-1">mcp:read</code> to start; <code className="rounded bg-muted px-1">mcp:write</code> if you want the agent to upload or start workflows.
          </div>
        ) : (
          <ul className="space-y-2">
            {mcpKeys.map((k) => (
              <li
                key={k.key_id}
                className="flex items-center justify-between rounded-lg border border-border bg-card p-4"
                data-testid={`mcp-key-row-${k.key_id}`}
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
                  </p>
                </div>
                <Button
                  size="sm" variant="outline"
                  onClick={() => {
                    if (confirm(`Revoke "${k.name}"? Any LLM agent using it will fail next call.`)) {
                      revoke.mutate(k.key_id)
                    }
                  }}
                >
                  <Trash2 className="me-1 h-4 w-4" /> Revoke
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* ---- Install snippets ----------------------------------------- */}
      <section className="mb-6">
        <h2 className="mb-3 text-lg font-semibold">Install snippets</h2>
        <p className="mb-3 text-sm text-muted-foreground">
          Replace <code className="rounded bg-muted px-1">{`{{API_KEY}}`}</code> with your key value (shown once at creation). Restart the client after editing.
        </p>
        <Snippet
          title="Claude Desktop"
          file="~/Library/Application Support/Claude/claude_desktop_config.json (macOS) — ~/AppData/Roaming/Claude/claude_desktop_config.json (Windows)"
          code={claudeDesktopSnippet(mcpURL)}
        />
        <Snippet
          title="Cursor"
          file="Settings → MCP → Add server"
          code={cursorSnippet(mcpURL)}
        />
        <Snippet
          title="GitHub Copilot (custom MCP server)"
          file="VS Code settings.json or .vscode/mcp.json"
          code={copilotSnippet(mcpURL)}
        />
      </section>

      <CreateMCPKeyDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(plaintext, id) => {
          setRevealedKey({ id, plaintext })
          qc.invalidateQueries({ queryKey: ['api-keys'] })
        }}
      />

      {revealedKey && (
        <RevealedKeyDialog
          plaintext={revealedKey.plaintext}
          onClose={() => setRevealedKey(null)}
        />
      )}
    </div>
  )
}

// ---- Snippet card ---------------------------------------------------

function Snippet({ title, file, code }: { title: string; file: string; code: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="mb-3 rounded-lg border border-border bg-card p-3">
      <div className="mb-1 flex items-center justify-between">
        <span className="text-sm font-medium">{title}</span>
        <Button
          size="sm" variant="ghost"
          onClick={() => copyText(code).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) })}
        >
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
          <span className="ms-1 text-xs">{copied ? 'Copied' : 'Copy'}</span>
        </Button>
      </div>
      <p className="mb-1 text-xs text-muted-foreground">Add to: <code className="font-mono">{file}</code></p>
      <pre className="overflow-x-auto rounded bg-muted/60 p-2 text-xs">{code}</pre>
    </div>
  )
}

function claudeDesktopSnippet(url: string): string {
  return `{
  "mcpServers": {
    "vaultdms": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-everything-http", "${url}"],
      "env": {
        "MCP_BEARER": "{{API_KEY}}"
      }
    }
  }
}`
}

function cursorSnippet(url: string): string {
  return `{
  "mcpServers": {
    "vaultdms": {
      "url": "${url}/sse",
      "headers": {
        "Authorization": "Bearer {{API_KEY}}"
      }
    }
  }
}`
}

function copilotSnippet(url: string): string {
  return `{
  "mcp.servers": {
    "vaultdms": {
      "type": "sse",
      "url": "${url}/sse",
      "headers": {
        "Authorization": "Bearer {{API_KEY}}"
      }
    }
  }
}`
}

// ---- Create + reveal dialogs ----------------------------------------

function CreateMCPKeyDialog({ open, onOpenChange, onCreated }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (plaintext: string, id: string) => void
}) {
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<string[]>(['mcp:read'])

  const create = useAppMutation({
    mutationFn: () => createAPIKey({ name: name.trim(), scopes }),
    onSuccess: (data) => {
      onCreated(data.api_key, data.key_id)
      onOpenChange(false)
      setName('')
      setScopes(['mcp:read'])
    },
    onError: (e: Error) => toast.error(e.message || 'Create failed'),
  })

  const toggle = (id: string) => setScopes((s) => s.includes(id) ? s.filter((x) => x !== id) : [...s, id])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>New MCP key</DialogTitle>
          <DialogDescription>
            Shown once at creation. Paste into your Claude Desktop / Cursor / Copilot config.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div>
            <label className="mb-1 block text-sm font-medium">Name</label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Claude Desktop on my laptop" />
          </div>
          <div>
            <label className="mb-1 block text-sm font-medium">Scopes</label>
            <div className="space-y-1">
              {MCP_SCOPES.map((s) => (
                <label key={s.id} className="flex cursor-pointer items-start gap-2 text-sm">
                  <input type="checkbox" checked={scopes.includes(s.id)} onChange={() => toggle(s.id)} className="mt-0.5" />
                  <div>
                    <code className="font-mono text-xs">{s.label}</code>
                    <div className="text-xs text-muted-foreground">{s.desc}</div>
                  </div>
                </label>
              ))}
            </div>
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={() => create.mutate()} disabled={name.trim() === '' || scopes.length === 0 || create.isPending}>
            {create.isPending ? 'Creating…' : 'Create'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function RevealedKeyDialog({ plaintext, onClose }: { plaintext: string; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose() }}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Copy your MCP key now</DialogTitle>
          <DialogDescription>This is the only time we'll show this value.</DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2 py-2">
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
        <DialogFooter>
          <Button onClick={onClose}>I've saved it</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
