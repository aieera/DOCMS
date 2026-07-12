import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Copy, RefreshCw, KeyRound, UserPlus, UserMinus, UserCog, Info } from 'lucide-react'

import { getScimInfo, rotateScimToken, getScimLog, type ScimLogEntry } from '@/api/scim'
import { useAppMutation } from '@/hooks/useAppMutation'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'

function copy(text: string) {
  navigator.clipboard?.writeText(text).then(
    () => toast.success('Copied'),
    () => toast.error('Copy failed'),
  )
}

function ActionIcon({ action }: { action: ScimLogEntry['action'] }) {
  if (action === 'provisioned') return <UserPlus className="h-4 w-4 text-emerald-500" />
  if (action === 'deprovisioned' || action === 'deleted') return <UserMinus className="h-4 w-4 text-red-500" />
  return <UserCog className="h-4 w-4 text-blue-500" />
}

function ScimPage() {
  const qc = useQueryClient()
  const { data: info } = useQuery({ queryKey: ['scim-info'], queryFn: getScimInfo })
  const { data: log = [] } = useQuery({ queryKey: ['scim-log'], queryFn: getScimLog, refetchInterval: 10000 })
  const [freshToken, setFreshToken] = useState('')

  const rotate = useAppMutation({
    mutationFn: rotateScimToken,
    onSuccess: (token) => {
      setFreshToken(token)
      toast.success('New SCIM token generated — copy it now, it won’t be shown again')
      qc.invalidateQueries({ queryKey: ['scim-info'] })
    },
    defaultErrorMessage: 'Could not rotate token',
  })

  const baseURL = info ? `${window.location.origin}${info.base_path}` : ''

  return (
    <div className="mx-auto max-w-4xl p-6">
      <PageHeader
        title="SCIM provisioning"
        description="Automatic user + group provisioning from your IdP (SCIM 2.0). Point your IdP at the base URL below with the bearer token."
      />

      <div className="mt-6 space-y-3 rounded-lg border border-border bg-card p-4">
        <div>
          <label className="text-xs font-semibold uppercase text-muted-foreground">SCIM base URL</label>
          <div className="mt-1 flex items-center gap-2">
            <code className="flex-1 truncate rounded border border-border bg-background px-2 py-1.5 text-sm" data-testid="scim-base-url">
              {baseURL || '—'}
            </code>
            <Button size="sm" variant="outline" onClick={() => copy(baseURL)} disabled={!baseURL}>
              <Copy className="h-3.5 w-3.5" /> Copy
            </Button>
          </div>
        </div>

        <div>
          <label className="text-xs font-semibold uppercase text-muted-foreground">Bearer token</label>
          <div className="mt-1 flex items-center gap-2">
            {freshToken ? (
              <code className="flex-1 truncate rounded border border-amber-500/50 bg-amber-500/10 px-2 py-1.5 text-sm" data-testid="scim-token">
                {freshToken}
              </code>
            ) : (
              <span className="flex-1 text-sm text-muted-foreground">
                {info?.configured ? 'A token is configured (hidden). Rotate to issue a new one.' : 'No token configured yet.'}
              </span>
            )}
            {freshToken && (
              <Button size="sm" variant="outline" onClick={() => copy(freshToken)}><Copy className="h-3.5 w-3.5" /> Copy</Button>
            )}
            <Button size="sm" onClick={() => rotate.mutate(undefined)} disabled={rotate.isPending} data-testid="rotate-token">
              {rotate.isPending ? <RefreshCw className="h-3.5 w-3.5 animate-spin" /> : <KeyRound className="h-3.5 w-3.5" />}
              {info?.configured ? 'Rotate token' : 'Generate token'}
            </Button>
          </div>
        </div>
      </div>

      <div className="mt-4 flex items-start gap-2 rounded-lg border border-border bg-muted/20 p-3 text-xs text-muted-foreground">
        <Info className="mt-0.5 h-4 w-4 shrink-0" />
        <div>
          <p className="font-medium text-foreground">Attribute mapping</p>
          <p>userName / emails → email · displayName → display name · active:false (PATCH/PUT/DELETE) deprovisions (revokes sessions + API keys). Group membership syncs to SeDoc groups. Filtering + pagination follow RFC 7644.</p>
        </div>
      </div>

      <h3 className="mt-6 text-sm font-semibold">Recent provisioning activity</h3>
      <div className="mt-2 overflow-hidden rounded-lg border border-border">
        <table className="w-full text-sm">
          <thead className="bg-muted/40 text-start text-xs text-muted-foreground">
            <tr><th className="p-2 w-8" /><th className="p-2">Action</th><th className="p-2">Detail</th><th className="p-2">When</th></tr>
          </thead>
          <tbody>
            {log.map((e) => (
              <tr key={e.id} className="border-t border-border" data-testid={`scim-log-${e.id}`}>
                <td className="p-2"><ActionIcon action={e.action} /></td>
                <td className="p-2">{e.action} <span className="text-xs text-muted-foreground">{e.resource_type}</span></td>
                <td className="p-2 text-xs">{e.detail || e.external_id || '—'}</td>
                <td className="p-2 text-xs text-muted-foreground">{new Date(e.created_at).toLocaleString()}</td>
              </tr>
            ))}
            {log.length === 0 && <tr><td colSpan={4} className="p-3 text-center text-muted-foreground">No provisioning activity yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/scim')({
  component: ScimPage,
})
