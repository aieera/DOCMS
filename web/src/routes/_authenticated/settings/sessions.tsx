// /settings/sessions — dedicated active-sessions management page.
// Distinct from the SessionsCard in /admin/settings (which bundles MFA
// + sessions into one security tab); this page is a standalone surface
// linked from the session-warning banner and from the docs.

import { createFileRoute } from '@tanstack/react-router'
import { Trash2, Smartphone } from 'lucide-react'
import { useState } from 'react'
import toast from 'react-hot-toast'

import { PageHeader } from '@/components/shared/PageHeader'
import { useSessions, useRevokeSession, useRevokeAllOtherSessions } from '@/hooks/useSecurity'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { Button } from '@/components/ui/Button'
import { formatDeviceLine } from '@/lib/userAgent'

function SessionsPage() {
  const { data: sessions, isLoading, isError } = useSessions()
  const revoke = useRevokeSession()
  const revokeAll = useRevokeAllOtherSessions()
  const [confirmAll, setConfirmAll] = useState(false)

  return (
    <div className="space-y-6">
      <PageHeader
        title="Active sessions"
        description="Devices and browsers currently signed in to your account. Revoke any you don't recognise."
      />
      <div className="flex items-center justify-end">
        <Button
          variant="outline"
          data-testid="revoke-all-button"
          onClick={() => setConfirmAll(true)}
          disabled={!sessions || sessions.length <= 1}
        >
          Revoke all other sessions
        </Button>
      </div>

      {isLoading && (
        <p data-testid="sessions-loading" className="text-sm text-[var(--color-text-secondary)]">
          Loading sessions…
        </p>
      )}
      {isError && (
        <div role="alert" className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-900 dark:bg-red-950/40 dark:text-red-200">
          Failed to load sessions.
        </div>
      )}

      {sessions && sessions.length > 0 && (
        <table className="w-full text-sm" data-testid="sessions-table">
          <thead className="border-b border-[var(--color-border)] text-left text-xs uppercase text-[var(--color-text-secondary)]">
            <tr>
              <th className="py-2 ps-2">Device</th>
              <th className="py-2">Location</th>
              <th className="py-2">Last active</th>
              <th className="py-2">Created</th>
              <th className="py-2 pe-2 text-end">Actions</th>
            </tr>
          </thead>
          <tbody>
            {sessions.map((s) => (
              <tr key={s.id} data-testid={`session-row-${s.id}`} className="border-b border-[var(--color-border)]/60">
                <td className="py-2 ps-2">
                  <div className="flex items-center gap-2">
                    <Smartphone className="h-4 w-4 text-[var(--color-text-secondary)]" aria-hidden="true" />
                    <div>
                      <div className="font-medium">{formatDeviceLine(s.user_agent)}</div>
                      {s.is_current && (
                        <span data-testid="current-session-badge" className="text-xs text-emerald-700 dark:text-emerald-300">
                          Current session
                        </span>
                      )}
                    </div>
                  </div>
                </td>
                <td className="py-2">
                  {/* Backend stores IP only today; geo-lookup lives server-side via pkg/geo
                      and is plumbed in a follow-up. For now we surface the IP directly so the
                      user can recognise known networks. */}
                  <span className="text-xs text-[var(--color-text-secondary)]">{s.ip_address || '—'}</span>
                </td>
                <td className="py-2">{new Date(s.last_activity_at).toLocaleString()}</td>
                <td className="py-2">{new Date(s.created_at).toLocaleString()}</td>
                <td className="py-2 pe-2 text-end">
                  {!s.is_current && (
                    <Button
                      variant="ghost"
                      size="sm"
                      title="Revoke this session"
                      data-testid={`revoke-session-${s.id}`}
                      onClick={() => {
                        revoke.mutate(s.id, {
                          onSuccess: () => toast.success('Session revoked'),
                          onError: (e) => toast.error(`Revoke failed: ${String(e)}`),
                        })
                      }}
                      loading={revoke.isPending && revoke.variables === s.id}
                    >
                      <Trash2 className="h-4 w-4 text-red-500" />
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {sessions && sessions.length === 0 && (
        <p className="text-sm text-[var(--color-text-secondary)]">No active sessions.</p>
      )}

      <ConfirmDialog
        open={confirmAll}
        onOpenChange={setConfirmAll}
        title="Revoke all other sessions?"
        description="Every device except this one will be signed out immediately. You'll stay signed in here."
        confirmLabel="Revoke all"
        destructive
        loading={revokeAll.isPending}
        onConfirm={() =>
          revokeAll.mutate(undefined, {
            onSuccess: (res) => {
              toast.success(`Revoked ${res.revoked} session${res.revoked === 1 ? '' : 's'}`)
              setConfirmAll(false)
            },
            onError: (e) => toast.error(`Revoke failed: ${String(e)}`),
          })
        }
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/settings/sessions')({
  component: SessionsPage,
})
