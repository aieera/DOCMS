import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Dialog } from '@/components/ui/Dialog'
import { Skeleton } from '@/components/ui/Skeleton'
import { EmptyState } from '@/components/ui/EmptyState'
import { Key, Copy, Trash2 } from 'lucide-react'
import toast from 'react-hot-toast'
import { useAPIKeys, useCreateAPIKey, useRevokeAPIKey } from '@/hooks/useSecurity'
import type { APIKeyIssued } from '@/api/security'
import { formatRelativeTime } from '@/lib/formatters'

function ApiKeysPage() {
  const { data: keys, isLoading } = useAPIKeys()
  const createMut = useCreateAPIKey()
  const revokeMut = useRevokeAPIKey()

  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState('')
  const [expiresDays, setExpiresDays] = useState('90')
  const [issued, setIssued] = useState<APIKeyIssued | null>(null)

  const handleCreate = async () => {
    if (!name.trim()) {
      toast.error('Name is required')
      return
    }
    try {
      const k = await createMut.mutateAsync({
        name: name.trim(),
        scopes: ['documents:read', 'documents:write'],
        expires_in_days: Number(expiresDays) || 90,
      })
      setIssued(k)
      setName('')
    } catch {
      toast.error('Failed to create API key')
    }
  }

  const handleRevoke = async (id: string) => {
    if (!confirm('Revoke this API key? This cannot be undone.')) return
    try {
      await revokeMut.mutateAsync(id)
      toast.success('API key revoked')
    } catch {
      toast.error('Failed to revoke key')
    }
  }

  return (
    <div>
      <PageHeader
        title="API Keys"
        description="Manage API access tokens"
        actions={
          <Button onClick={() => { setIssued(null); setShowCreate(true) }} data-testid="new-api-key">
            <Key className="mr-1 h-4 w-4" /> New API key
          </Button>
        }
      />

      {isLoading ? (
        <Skeleton className="h-40" />
      ) : !keys || keys.length === 0 ? (
        <EmptyState
          icon={<Key className="h-10 w-10 text-muted-foreground" />}
          title="No API keys"
          description="Generate API keys to allow programmatic access."
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-[var(--color-border)]">
          <table className="w-full text-sm">
            <thead className="bg-[var(--color-bg-secondary)] text-left">
              <tr>
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Prefix</th>
                <th className="px-4 py-2 font-medium">Created</th>
                <th className="px-4 py-2 font-medium">Last used</th>
                <th className="px-4 py-2 font-medium">Expires</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.key_id} className="border-t border-[var(--color-border)]">
                  <td className="px-4 py-2">{k.name}</td>
                  <td className="px-4 py-2 font-mono text-xs">{k.key_prefix}…</td>
                  <td className="px-4 py-2 text-[var(--color-text-secondary)]">{formatRelativeTime(k.created_at)}</td>
                  <td className="px-4 py-2 text-[var(--color-text-secondary)]">
                    {k.last_used_at ? formatRelativeTime(k.last_used_at) : '—'}
                  </td>
                  <td className="px-4 py-2 text-[var(--color-text-secondary)]">
                    {k.expires_at ? formatRelativeTime(k.expires_at) : 'Never'}
                  </td>
                  <td className="px-4 py-2 text-right">
                    <Button variant="ghost" size="sm" onClick={() => handleRevoke(k.key_id)} aria-label="Revoke">
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Dialog
        open={showCreate}
        onOpenChange={(o) => { setShowCreate(o); if (!o) setIssued(null) }}
        title={issued ? 'API key created' : 'Create API key'}
        size="md"
      >
        {issued ? (
          <div className="space-y-3">
            <p className="text-sm">
              Copy this token now — <strong>it will not be shown again</strong>.
            </p>
            <div className="flex items-center gap-2 rounded-md border border-[var(--color-border)] bg-slate-50 p-2 dark:bg-slate-800">
              <code className="flex-1 truncate font-mono text-xs">{issued.api_key}</code>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => { navigator.clipboard.writeText(issued.api_key); toast.success('Copied') }}
              >
                <Copy className="h-4 w-4" />
              </Button>
            </div>
            {issued.warning && <p className="text-xs text-amber-600">{issued.warning}</p>}
            <div className="flex justify-end">
              <Button onClick={() => { setShowCreate(false); setIssued(null) }}>Done</Button>
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <Input label="Name" placeholder="e.g. CI pipeline" value={name} onChange={(e) => setName(e.target.value)} />
            <Input
              label="Expires in (days)"
              type="number"
              min="1"
              max="3650"
              value={expiresDays}
              onChange={(e) => setExpiresDays(e.target.value)}
            />
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => setShowCreate(false)}>Cancel</Button>
              <Button onClick={handleCreate} disabled={createMut.isPending}>
                {createMut.isPending ? 'Creating…' : 'Create'}
              </Button>
            </div>
          </div>
        )}
      </Dialog>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/api-keys')({ component: ApiKeysPage })
