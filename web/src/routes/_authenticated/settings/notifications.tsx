import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Bell, FileText, Workflow, ClipboardCheck, ShieldOff, ShieldCheck, Cog } from 'lucide-react'
import toast from 'react-hot-toast'

import {
  getNotificationPreferences,
  updateNotificationPreferences,
  type NotificationChannel,
  type NotificationCategory,
  type NotificationPreferences,
} from '@/api/notifications'
import { Button } from '@/components/ui/Button'

const CATEGORIES: { id: NotificationCategory; label: string; hint: string; icon: typeof Bell }[] = [
  { id: 'document', label: 'Documents', hint: 'Share-link activity, comments, version uploads.', icon: FileText },
  { id: 'workflow', label: 'Workflows', hint: 'Review assignments + completion.', icon: Workflow },
  { id: 'acknowledgement', label: 'Acknowledgements', hint: 'Policy attestations assigned to you.', icon: ClipboardCheck },
  { id: 'quarantine', label: 'Quarantine', hint: 'Admin-only: virus / MIME-block alerts.', icon: ShieldOff },
  { id: 'security', label: 'Security', hint: 'Binding-mismatch, session revocation, MFA events.', icon: ShieldCheck },
  { id: 'system', label: 'System', hint: 'Planned maintenance + platform announcements.', icon: Cog },
]

const CHANNELS: { id: NotificationChannel; label: string }[] = [
  { id: 'in_app', label: 'In-app' },
  { id: 'email', label: 'Email' },
  { id: 'slack', label: 'Slack' },
  { id: 'webhook', label: 'Webhook' },
]

const DEFAULT_PREFS: NotificationPreferences = {
  preferences: {
    document: ['in_app', 'email'],
    workflow: ['in_app', 'email'],
    acknowledgement: ['in_app', 'email'],
    quarantine: ['in_app'],
    security: ['in_app', 'email'],
    system: ['in_app'],
  },
  digest: 'immediate',
}

function NotificationSettingsPage() {
  const qc = useQueryClient()
  const { data, isLoading, isError } = useQuery({
    queryKey: ['notifications', 'preferences'],
    queryFn: getNotificationPreferences,
  })
  const [form, setForm] = useState<NotificationPreferences>(DEFAULT_PREFS)
  useEffect(() => {
    if (data) setForm(data)
  }, [data])

  const save = useMutation({
    mutationFn: (p: NotificationPreferences) => updateNotificationPreferences(p),
    onSuccess: () => {
      toast.success('Notification preferences saved')
      qc.invalidateQueries({ queryKey: ['notifications', 'preferences'] })
    },
    onError: () => toast.error('Save failed'),
  })

  const toggle = (cat: NotificationCategory, ch: NotificationChannel) => {
    setForm((f) => {
      const current = f.preferences[cat] ?? []
      const next = current.includes(ch) ? current.filter((c) => c !== ch) : [...current, ch]
      return { ...f, preferences: { ...f.preferences, [cat]: next } }
    })
  }

  if (isLoading) {
    return <p className="text-sm text-[var(--color-text-secondary)]">Loading preferences…</p>
  }
  if (isError) {
    return (
      <div role="alert" className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-900 dark:border-red-900 dark:bg-red-950/40 dark:text-red-200">
        Failed to load notification preferences.
      </div>
    )
  }

  return (
    <form
      data-testid="notification-preferences-form"
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate(form)
      }}
      className="space-y-8"
    >
      <section>
        <h2 className="text-base font-semibold">Categories</h2>
        <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
          Pick the channels each category delivers to. An empty row silences that category entirely.
        </p>
        <div className="mt-4 overflow-hidden rounded-lg border border-[var(--color-border)]">
          <table className="w-full text-sm">
            <thead className="bg-[var(--color-bg-secondary)] text-left text-xs uppercase text-[var(--color-text-secondary)]">
              <tr>
                <th className="px-3 py-2">Category</th>
                {CHANNELS.map((c) => (
                  <th key={c.id} className="px-3 py-2 text-center">{c.label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {CATEGORIES.map(({ id, label, hint, icon: Icon }) => (
                <tr key={id} className="border-t border-[var(--color-border)]" data-testid={`category-row-${id}`}>
                  <td className="px-3 py-3">
                    <div className="flex items-start gap-2">
                      <Icon className="mt-0.5 h-4 w-4 shrink-0 text-[var(--color-text-secondary)]" aria-hidden="true" />
                      <div>
                        <div className="font-medium">{label}</div>
                        <div className="text-xs text-[var(--color-text-secondary)]">{hint}</div>
                      </div>
                    </div>
                  </td>
                  {CHANNELS.map((c) => {
                    const on = (form.preferences[id] ?? []).includes(c.id)
                    return (
                      <td key={c.id} className="px-3 py-3 text-center">
                        <input
                          type="checkbox"
                          data-testid={`toggle-${id}-${c.id}`}
                          checked={on}
                          onChange={() => toggle(id, c.id)}
                          aria-label={`${label} via ${c.label}`}
                        />
                      </td>
                    )
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="max-w-md">
        <h2 className="text-base font-semibold">Delivery cadence</h2>
        <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
          Controls email + in-app batching. Security alerts always deliver immediately regardless of this setting.
        </p>
        <div className="mt-4 space-y-2">
          {(['immediate', 'hourly', 'daily'] as const).map((v) => (
            <label key={v} className="flex items-start gap-2 text-sm">
              <input
                type="radio"
                name="digest"
                value={v}
                data-testid={`digest-${v}`}
                checked={form.digest === v}
                onChange={() => setForm((f) => ({ ...f, digest: v }))}
                className="mt-0.5"
              />
              <span>
                <strong className="capitalize">{v}</strong>{' '}
                <span className="text-[var(--color-text-secondary)]">— {digestHint(v)}</span>
              </span>
            </label>
          ))}
        </div>
      </section>

      <div>
        <Button type="submit" loading={save.isPending} data-testid="save-notification-preferences">
          Save preferences
        </Button>
      </div>
    </form>
  )
}

function digestHint(v: 'immediate' | 'hourly' | 'daily'): string {
  switch (v) {
    case 'immediate':
      return 'Every event generates a separate notification.'
    case 'hourly':
      return 'Batched into one digest per hour. Recommended for active users.'
    case 'daily':
      return 'One digest per day at 09:00 local time.'
  }
}

export const Route = createFileRoute('/_authenticated/settings/notifications')({
  component: NotificationSettingsPage,
})
