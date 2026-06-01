import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { type ColumnDef } from '@tanstack/react-table'
import { Key, Copy, Trash2, AlertTriangle } from 'lucide-react'
import { toast } from 'sonner'

import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Dialog } from '@/components/ui/Dialog'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { EmptyState } from '@/components/ui/EmptyState'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/shadcn/badge'
import { DataTable } from '@/components/ui/DataTable'
import { useAPIKeys, useCreateAPIKey, useRevokeAPIKey } from '@/hooks/useSecurity'
import type { APIKeyIssued } from '@/api/security'
import { formatRelativeTime } from '@/lib/formatters'

interface APIKeyRow {
  key_id: string
  name: string
  key_prefix: string
  created_at: string
  last_used_at?: string | null
  expires_at?: string | null
  revoked_at?: string | null
}

function ApiKeysPage() {
  const { data: keys, isLoading } = useAPIKeys()
  const createMut = useCreateAPIKey()
  const revokeMut = useRevokeAPIKey()

  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState('')
  const [expiresDays, setExpiresDays] = useState('90')
  const [issued, setIssued] = useState<APIKeyIssued | null>(null)
  const [revokeId, setRevokeId] = useState<string | null>(null)

  const handleCreate = async () => {
    if (!name.trim()) {
      toast.error('Name is required'); return
    }
    try {
      const k = await createMut.mutateAsync({
        name: name.trim(),
        scopes: ['documents:read', 'documents:write'],
        expires_in_days: Number(expiresDays) || 90,
      })
      setIssued(k); setName('')
    } catch {
      toast.error('Failed to create API key')
    }
  }

  const handleRevoke = async () => {
    if (!revokeId) return
    try {
      await revokeMut.mutateAsync(revokeId)
      toast.success('API key revoked')
    } catch {
      toast.error('Failed to revoke key')
    } finally {
      setRevokeId(null)
    }
  }

  const columns: ColumnDef<APIKeyRow, unknown>[] = [
    { accessorKey: 'name', header: 'Name', cell: ({ row }) => <span className="text-sm font-medium">{row.original.name}</span> },
    { accessorKey: 'key_prefix', header: 'Prefix', cell: ({ row }) => <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{row.original.key_prefix}…</code> },
    { accessorKey: 'created_at', header: 'Created', cell: ({ row }) => <span className="text-xs text-muted-foreground">{formatRelativeTime(row.original.created_at)}</span> },
    { accessorKey: 'last_used_at', header: 'Last used', cell: ({ row }) => <span className="text-xs text-muted-foreground">{row.original.last_used_at ? formatRelativeTime(row.original.last_used_at) : '—'}</span> },
    { accessorKey: 'expires_at', header: 'Expires', cell: ({ row }) => <span className="text-xs text-muted-foreground">{row.original.expires_at ? formatRelativeTime(row.original.expires_at) : 'Never'}</span> },
    {
      id: 'status',
      header: 'Status',
      cell: ({ row }) => {
        const k = row.original
        if (k.revoked_at) return <Badge variant="destructive">Revoked</Badge>
        if (k.expires_at && new Date(k.expires_at).getTime() < Date.now()) {
          return <Badge variant="secondary">Expired</Badge>
        }
        return <Badge variant="default">Active</Badge>
      },
    },
    {
      id: 'actions',
      header: '',
      cell: ({ row }) => (
        <div className="flex justify-end">
          {!row.original.revoked_at && (
            <Button variant="ghost" size="sm" onClick={() => setRevokeId(row.original.key_id)} aria-label="Revoke">
              <Trash2 className="h-4 w-4 text-destructive" />
            </Button>
          )}
        </div>
      ),
    },
  ]

  return (
    <div className="space-y-6">
      <PageHeader
        title="API keys"
        description="Server-to-server tokens for programmatic access. Each key is scoped, audited, and can be revoked at any time."
        actions={
          <Button onClick={() => { setIssued(null); setShowCreate(true) }} data-testid="new-api-key">
            <Key className="h-4 w-4" /> New API key
          </Button>
        }
      />

      {!isLoading && (!keys || keys.length === 0) ? (
        <EmptyState
          icon={<Key className="h-6 w-6" />}
          title="No API keys"
          description="Generate an API key to allow programmatic access from CI, scripts, or partner systems."
          actionLabel="Create your first key"
          onAction={() => setShowCreate(true)}
        />
      ) : (
        <DataTable
          columns={columns}
          data={(keys ?? []) as APIKeyRow[]}
          isLoading={isLoading}
          emptyState="No API keys."
        />
      )}

      <Dialog
        open={showCreate}
        onOpenChange={(o) => { setShowCreate(o); if (!o) setIssued(null) }}
        title={issued ? 'API key created' : 'Create API key'}
        description={issued ? 'Copy the secret below — it will not be shown again.' : 'Pick a memorable name and an expiration window.'}
       
      >
        {issued ? (
          <div className="space-y-4">
            <Card className="space-y-2 p-3">
              <div className="flex items-center gap-2">
                <code className="flex-1 truncate rounded bg-muted px-2 py-1.5 font-mono text-xs">{issued.api_key}</code>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => { navigator.clipboard.writeText(issued.api_key); toast.success('Copied') }}
                  aria-label="Copy"
                >
                  <Copy className="h-4 w-4" />
                </Button>
              </div>
            </Card>
            {issued.warning && (
              <div className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs text-warning">
                <AlertTriangle className="h-4 w-4 shrink-0" />
                <span>{issued.warning}</span>
              </div>
            )}
            <div className="flex justify-end">
              <Button onClick={() => { setShowCreate(false); setIssued(null) }}>Done</Button>
            </div>
          </div>
        ) : (
          <form
            onSubmit={(e) => { e.preventDefault(); handleCreate() }}
            className="space-y-4"
          >
            <Input label="Name" placeholder="e.g. CI pipeline" value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
            <Input
              label="Expires in (days)"
              type="number"
              min="1"
              max="3650"
              value={expiresDays}
              onChange={(e) => setExpiresDays(e.target.value)}
            />
            <div className="flex justify-end gap-2 pt-2">
              <Button type="button" variant="ghost" onClick={() => setShowCreate(false)}>Cancel</Button>
              <Button type="submit" loading={createMut.isPending}>Create key</Button>
            </div>
          </form>
        )}
      </Dialog>

      <ConfirmDialog
        open={!!revokeId}
        onOpenChange={(o) => !o && setRevokeId(null)}
        title="Revoke API key?"
        description="Any client using this key will start getting 401 errors immediately. This cannot be undone."
        confirmLabel="Revoke"
        destructive
        loading={revokeMut.isPending}
        onConfirm={handleRevoke}
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/api-keys')({ component: ApiKeysPage })
