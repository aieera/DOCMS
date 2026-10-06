import { copyText } from '@/lib/clipboard'
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
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
import { formatDateTime } from '@/lib/formatters'
export const Route = createFileRoute('/_authenticated/admin/integrations/mcp')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/integrations', search: { tab: 'mcp' }, replace: true })
  },
})

// The scope id IS its label — it's the literal string sent in the token.
const MCP_SCOPES = [
  { id: 'mcp:read',  descKey: 'scope_read_desc' },
  { id: 'mcp:write', descKey: 'scope_write_desc' },
  { id: 'mcp:*',     descKey: 'scope_all_desc' },
]

export function MCPPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const keysQ = useQuery({ queryKey: ['api-keys'], queryFn: listAPIKeys })
  const toolsQ = useQuery({ queryKey: ['mcp-tools'], queryFn: listMCPTools, refetchInterval: 60_000 })
  const [createOpen, setCreateOpen] = useState(false)
  const [revealedKey, setRevealedKey] = useState<{ id: string; plaintext: string } | null>(null)

  const revoke = useAppMutation({
    mutationFn: revokeAPIKey,
    onSuccess: () => {
      toast.success(t('adm.mcp.revoked'))
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
        title={t('adm.mcp.title')}
        description={t('adm.mcp.description')}
      />

      {/* ---- Server status -------------------------------------------- */}
      <section className="mb-6">
        <h2 className="mb-2 text-lg font-semibold">{t('adm.mcp.server')}</h2>
        <div className="flex items-center justify-between rounded-lg bg-card p-4 shadow-neu">
          <div className="flex-1">
            <div className="flex items-center gap-2">
              <Bot className="h-4 w-4" />
              <span className="font-medium">vaultdms-mcp</span>
              {toolsQ.isLoading ? (
                <span className="text-xs text-muted-foreground">{t('adm.mcp.checking')}</span>
              ) : (toolsQ.data?.length ?? 0) > 0 ? (
                <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs text-foreground">
                  {t('adm.mcp.reachable', { count: toolsQ.data?.length ?? 0 })}
                </span>
              ) : (
                <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs">
                  {t('adm.mcp.unreachable')}
                </span>
              )}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {t('adm.mcp.endpoint')}<code dir="ltr" className="rounded bg-muted px-1 font-mono">{mcpURL}</code>
            </p>
          </div>
        </div>
        {(toolsQ.data?.length ?? 0) > 0 && (
          <ul className="mt-2 grid grid-cols-1 gap-2 sm:grid-cols-2">
            {toolsQ.data!.map((tool) => (
              <li key={tool.name} className="rounded-lg bg-card/40 p-2 text-xs shadow-neu-sm">
                <code dir="ltr" className="font-mono font-semibold">{tool.name}</code>
                <p className="mt-0.5 text-muted-foreground">{tool.description}</p>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* ---- API keys ------------------------------------------------- */}
      <section className="mb-8" data-testid="mcp-api-keys-section">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-lg font-semibold">{t('adm.mcp.keys')}</h2>
          <Button size="sm" onClick={() => setCreateOpen(true)} data-testid="create-mcp-key">
            <Plus className="me-1 h-4 w-4" /> {t('adm.mcp.new_key')}
          </Button>
        </div>

        {keysQ.isLoading ? <Spinner /> : mcpKeys.length === 0 ? (
          <div className="rounded-lg border border-dashed border-border bg-card/40 p-6 text-center text-sm text-muted-foreground">
            <Trans
              i18nKey="adm.mcp.no_keys"
              ns="common"
              components={Array.from({ length: 2 }, (_, i) => (
                <code key={i} dir="ltr" className="rounded bg-muted px-1" />
              ))}
            />
          </div>
        ) : (
          <ul className="space-y-2">
            {mcpKeys.map((k) => (
              <li
                key={k.key_id}
                className="flex items-center justify-between rounded-lg bg-card p-4 shadow-neu"
                data-testid={`mcp-key-row-${k.key_id}`}
              >
                <div className="flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{k.name}</span>
                    <code dir="ltr" className="rounded bg-muted px-2 py-0.5 font-mono text-xs">{k.key_prefix}…</code>
                  </div>
                  <div className="mt-1 flex flex-wrap gap-1 text-xs">
                    {(k.scopes ?? []).map((s) => (
                      <span key={s} dir="ltr" className="rounded-full bg-muted px-2 py-0.5 font-mono">{s}</span>
                    ))}
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t('adm.mcp.created_at', { date: formatDateTime(k.created_at) })}
                    {k.last_used_at && t('adm.mcp.last_used', { date: formatDateTime(k.last_used_at) })}
                  </p>
                </div>
                <Button
                  size="sm" variant="outline"
                  onClick={() => {
                    if (confirm(t('adm.mcp.revoke_confirm', { name: k.name }))) {
                      revoke.mutate(k.key_id)
                    }
                  }}
                >
                  <Trash2 className="me-1 h-4 w-4" /> {t('adm.mcp.revoke')}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* ---- Install snippets ----------------------------------------- */}
      <section className="mb-6">
        <h2 className="mb-3 text-lg font-semibold">{t('adm.mcp.snippets')}</h2>
        <p className="mb-3 text-sm text-muted-foreground">
          {/* {{API_KEY}} is literal snippet text, so it rides through on a
              distinct {{placeholder}} slot rather than being interpolated. */}
          <Trans
            i18nKey="adm.mcp.snippets_hint"
            ns="common"
            values={{ placeholder: '{{API_KEY}}' }}
            components={[<code key="k" dir="ltr" className="rounded bg-muted px-1" />]}
          />
        </p>
        <Snippet
          title="Claude Desktop"
          file="~/Library/Application Support/Claude/claude_desktop_config.json (macOS) — ~/AppData/Roaming/Claude/claude_desktop_config.json (Windows)"
          code={claudeDesktopSnippet(mcpURL)}
        />
        <Snippet
          title="Cursor"
          file={t('adm.mcp.cursor_file')}
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
  const { t } = useTranslation('common')
  const [copied, setCopied] = useState(false)
  return (
    <div className="mb-3 rounded-lg bg-card p-3 shadow-neu">
      <div className="mb-1 flex items-center justify-between">
        <span className="text-sm font-medium">{title}</span>
        <Button
          size="sm" variant="ghost"
          onClick={() => copyText(code).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) })}
        >
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
          <span className="ms-1 text-xs">{copied ? t('adm.mcp.copied') : t('adm.mcp.copy')}</span>
        </Button>
      </div>
      <p className="mb-1 text-xs text-muted-foreground">{t('adm.mcp.add_to')}<code dir="ltr" className="font-mono">{file}</code></p>
      <pre className="overflow-x-auto rounded-lg bg-muted p-2 text-xs shadow-neu-inset">{code}</pre>
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
  const { t } = useTranslation('common')
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
    onError: (e: Error) => toast.error(e.message || t('adm.mcp.err_create')),
  })

  const toggle = (id: string) => setScopes((s) => s.includes(id) ? s.filter((x) => x !== id) : [...s, id])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('adm.mcp.dlg_title')}</DialogTitle>
          <DialogDescription>
            {t('adm.mcp.dlg_desc')}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div>
            <label className="mb-1 block text-sm font-medium">{t('adm.mcp.name')}</label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('adm.mcp.name_ph')} />
          </div>
          <div>
            <label className="mb-1 block text-sm font-medium">{t('adm.mcp.scopes')}</label>
            <div className="space-y-1">
              {MCP_SCOPES.map((s) => (
                <label key={s.id} className="flex cursor-pointer items-start gap-2 text-sm">
                  <input type="checkbox" checked={scopes.includes(s.id)} onChange={() => toggle(s.id)} className="mt-0.5" />
                  <div>
                    <code dir="ltr" className="font-mono text-xs">{s.id}</code>
                    <div className="text-xs text-muted-foreground">{t(`adm.mcp.${s.descKey}`)}</div>
                  </div>
                </label>
              ))}
            </div>
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>{t('adm.mcp.cancel')}</Button>
          <Button onClick={() => create.mutate()} disabled={name.trim() === '' || scopes.length === 0 || create.isPending}>
            {create.isPending ? t('adm.mcp.creating') : t('adm.mcp.create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function RevealedKeyDialog({ plaintext, onClose }: { plaintext: string; onClose: () => void }) {
  const { t } = useTranslation('common')
  const [copied, setCopied] = useState(false)
  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose() }}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('adm.mcp.reveal_title')}</DialogTitle>
          <DialogDescription>{t('adm.mcp.reveal_desc')}</DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2 py-2">
          <code className="flex-1 truncate rounded-lg bg-muted px-3 py-2 font-mono text-xs shadow-neu-inset">
            {plaintext}
          </code>
          <Button
            size="sm"
            onClick={() => copyText(plaintext).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) })}
          >
            {copied ? <Check className="me-1 h-4 w-4" /> : <Copy className="me-1 h-4 w-4" />}
            {copied ? t('adm.mcp.copied') : t('adm.mcp.copy')}
          </Button>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>{t('adm.mcp.saved')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
