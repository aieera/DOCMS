import { PasswordInput } from '@/components/ui/PasswordInput'
import { copyText } from '@/lib/clipboard'
import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { AtSign, Copy, Link2, ShieldAlert, UserPlus, X } from 'lucide-react'
import { toast } from 'sonner'
import { createShareLink } from '@/api/shareLinks'
import { createZTShare } from '@/api/ztShare'
import { getVersions } from '@/api/documents'
import { listUserDirectory, type DirectoryUser } from '@/api/auth'
import { grantPermission } from '@/api/permissions'
import { readErrorMessage } from '@/api/client'
import { useAuthStore } from '@/store/authStore'

interface Props { open: boolean; onOpenChange: (o: boolean) => void; documentId: string; documentTitle: string }

const expiryOptions = [
  { value: '24', label: '1 day' },
  { value: '168', label: '7 days' },
  { value: '720', label: '30 days' },
  { value: '8760', label: '1 year' },
]

const permissionOptions: Record<string, string[]> = {
  view: ['view'],
  download: ['view', 'download'],
  edit: ['view', 'download', 'edit'],
}

type ShareMode = 'people' | 'classic' | 'zt'

// Internal-share capability levels (policy-service grants — the same
// vocabulary Manage access uses).
const PEOPLE_CAPABILITIES = [
  { value: 'view', label: 'Viewer — can view' },
  { value: 'share', label: 'Commenter — can view and share' },
  { value: 'edit', label: 'Editor — can view, edit, and share' },
] as const

export function ShareDialog({ open, onOpenChange, documentId, documentTitle }: Props) {
  const qc = useQueryClient()
  const selfId = useAuthStore((s) => s.user?.id)
  const [expiryHours, setExpiryHours] = useState('168')
  const [password, setPassword] = useState('')
  const [permission, setPermission] = useState<keyof typeof permissionOptions>('view')
  const [shareUrl, setShareUrl] = useState('')
  const [creating, setCreating] = useState(false)
  // Default mode: share directly with people inside the DMS — the
  // grant lands in the recipient's "Shared with me". Links are the
  // secondary path for external recipients.
  const [mode, setMode] = useState<ShareMode>('people')
  const [ztRecipientEmail, setZtRecipientEmail] = useState('')
  const [ztMaxViews, setZtMaxViews] = useState('0')

  // ---- people mode state ----
  const [peopleQuery, setPeopleQuery] = useState('')
  const [selected, setSelected] = useState<DirectoryUser[]>([])
  const [capability, setCapability] = useState<'view' | 'share' | 'edit'>('view')
  // The directory endpoint (not /admin/users) — members can share too.
  const usersQ = useQuery({
    queryKey: ['user-directory'],
    queryFn: () => listUserDirectory(),
    enabled: open && mode === 'people',
    staleTime: 60_000,
  })
  // "@jas" and "jas" both match Jasmin — the @ is a trigger, not part
  // of the name.
  const term = peopleQuery.replace(/^@/, '').trim().toLowerCase()
  const suggestions = useMemo(() => {
    if (!term) return []
    const chosen = new Set(selected.map((u) => u.id))
    return (usersQ.data ?? [])
      .filter((u) => u.id !== selfId && !chosen.has(u.id))
      .filter((u) =>
        (u.display_name ?? '').toLowerCase().includes(term) ||
        (u.email ?? '').toLowerCase().includes(term))
      .slice(0, 6)
  }, [term, usersQ.data, selected, selfId])

  const shareWithPeople = async () => {
    if (selected.length === 0) return
    setCreating(true)
    // Keep the reason, not just the name. A bare `catch {}` here made a
    // permissions refusal, a duplicate grant and a dropped connection all
    // render as the same unactionable "Could not share with X".
    const failed: { name: string; reason: string }[] = []
    for (const u of selected) {
      const name = u.display_name || u.email
      try {
        await grantPermission('document', documentId, 'user', u.id, capability)
      } catch (err: unknown) {
        failed.push({ name, reason: readErrorMessage(err) ?? 'the server rejected the request' })
      }
    }
    setCreating(false)
    qc.invalidateQueries({ queryKey: ['permissions', 'document', documentId] })
    if (failed.length === 0) {
      toast.success(`Shared with ${selected.length} ${selected.length === 1 ? 'person' : 'people'}`)
      setSelected([])
      onOpenChange(false)
      return
    }
    // One recipient — lead with why. Several — name them, then the first
    // reason, since a batch usually fails for one shared cause.
    toast.error(
      failed.length === 1
        ? `Could not share with ${failed[0].name}: ${failed[0].reason}`
        : `Could not share with ${failed.map((f) => f.name).join(', ')} — ${failed[0].reason}`,
    )
    const failedNames = new Set(failed.map((f) => f.name))
    setSelected((prev) => prev.filter((u) => failedNames.has(u.display_name || u.email)))
  }

  const handleCreate = async () => {
    setCreating(true)
    try {
      if (mode === 'zt') {
        if (!ztRecipientEmail) {
          toast.error('Recipient email required for zero-trust share')
          return
        }
        // Get the latest version_id — the ZT endpoint pins to a
        // specific version so the recipient never sees later edits.
        const versions = await getVersions(documentId)
        const latest = versions[0]
        if (!latest?.id) {
          toast.error('Document has no versions to share')
          return
        }
        const result = await createZTShare({
          document_id: documentId,
          version_id: latest.id,
          recipient_email: ztRecipientEmail,
          expires_in_hours: Number(expiryHours),
          max_views: Number(ztMaxViews) || 0,
        })
        setShareUrl(result.share_url)
        toast.success('Zero-trust share created')
        return
      }

      const link = await createShareLink(documentId, {
        password: password || undefined,
        expires_in_hours: Number(expiryHours),
        permissions: permissionOptions[permission],
      })
      if (!link.token) {
        toast.error('Share link created, but token missing from response')
        return
      }
      setShareUrl(`${window.location.origin}/shared/${link.token}`)
      toast.success('Share link created')
    } catch (err: unknown) {
      // Turn-1 follow-up: getVersions now THROWS UnknownListShapeError
      // on malformed list responses (Wave 5 pattern 3); creating a
      // share link can also fail mid-flight. readErrorMessage parses
      // the backend's envelope so the user sees the actual cause
      // ("link limit reached", "no versions to share", etc.) rather
      // than a generic 'Failed to create share link'.
      toast.error(readErrorMessage(err) ?? 'Failed to create share link')
    } finally {
      setCreating(false)
    }
  }

  const copyLink = () => {
    copyText(shareUrl)
    toast.success('Link copied to clipboard')
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={`Share "${documentTitle}"`}>
      <div className="space-y-4">
        <Select
          label="Share mode"
          value={mode}
          onValueChange={(v) => { setShareUrl(''); setMode(v as ShareMode) }}
          options={[
            { value: 'people', label: 'People in this DMS (@mention)' },
            { value: 'classic', label: 'Standard share link' },
            { value: 'zt', label: 'Zero-trust view-only (watermarked, audited)' },
          ]}
        />

        {mode === 'people' && (
          <>
            <div className="relative">
              <Input
                label="Add people"
                icon={<AtSign className="h-4 w-4" />}
                value={peopleQuery}
                onChange={(e) => setPeopleQuery(e.target.value)}
                placeholder="Type @ and a name or email…"
                autoComplete="off"
                data-testid="share-people-input"
              />
              {suggestions.length > 0 && (
                <ul className="absolute inset-x-0 top-full z-50 mt-1 overflow-hidden rounded-md bg-card shadow-neu" data-testid="share-people-suggestions">
                  {suggestions.map((u) => (
                    <li key={u.id}>
                      <button
                        type="button"
                        onClick={() => { setSelected((prev) => [...prev, u]); setPeopleQuery('') }}
                        className="flex w-full items-center gap-2 px-3 py-2 text-start text-sm hover:bg-muted/60"
                        data-testid={`share-people-option-${u.email}`}
                      >
                        <span className="grid h-6 w-6 shrink-0 place-items-center rounded-full bg-muted text-[10px] font-semibold uppercase">
                          {(u.display_name || u.email || '?').slice(0, 2)}
                        </span>
                        <span className="min-w-0">
                          <span className="block truncate font-medium">{u.display_name || u.email}</span>
                          <span className="block truncate text-xs text-muted-foreground">{u.email}</span>
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>

            {selected.length > 0 && (
              <div className="flex flex-wrap gap-1.5" data-testid="share-people-chips">
                {selected.map((u) => (
                  <span key={u.id} className="inline-flex items-center gap-1 rounded-full border border-border bg-muted/40 py-0.5 ps-2.5 pe-1 text-xs font-medium">
                    {u.display_name || u.email}
                    <button
                      type="button"
                      onClick={() => setSelected((prev) => prev.filter((x) => x.id !== u.id))}
                      aria-label={`Remove ${u.display_name || u.email}`}
                      className="rounded-full p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </span>
                ))}
              </div>
            )}

            <Select
              label="Permission"
              value={capability}
              onValueChange={(v) => setCapability(v as typeof capability)}
              options={PEOPLE_CAPABILITIES.map((c) => ({ value: c.value, label: c.label }))}
            />

            <Button
              onClick={shareWithPeople}
              disabled={creating || selected.length === 0}
              className="w-full"
              data-testid="share-people-submit"
            >
              <UserPlus className="h-4 w-4" />
              {creating
                ? 'Sharing…'
                : selected.length > 0
                  ? `Share with ${selected.length} ${selected.length === 1 ? 'person' : 'people'}`
                  : 'Share'}
            </Button>
            <p className="text-xs text-muted-foreground">
              Recipients see the document under <strong>Shared with me</strong> and get the access level you pick — no link involved.
            </p>
          </>
        )}

        {mode === 'zt' && (
          <div className="rounded-md border border-warning/40 bg-warning/10 p-3 text-xs text-warning-strong">
            <div className="flex items-start gap-2">
              <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0" />
              <div>
                <p className="font-semibold">
                  View-only mode
                </p>
                <p className="mt-1">
                  Blocks copy, print, and download. Every page is watermarked with the
                  recipient's email so leaks can be traced. <strong>Does not prevent
                  screenshots taken with the recipient's device.</strong>
                </p>
              </div>
            </div>
          </div>
        )}

        {mode === 'classic' && (
          <>
            <Select
              label="Permission"
              value={permission}
              onValueChange={(v) => setPermission(v as keyof typeof permissionOptions)}
              options={[
                { value: 'view', label: 'View only' },
                { value: 'download', label: 'View + Download' },
                { value: 'edit', label: 'Edit' },
              ]}
            />
            <PasswordInput
              label="Password (optional)"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="Leave empty for no password"
            />
          </>
        )}
        {mode === 'zt' && (
          <>
            <Input
              label="Recipient email"
              type="email"
              value={ztRecipientEmail}
              onChange={(e) => setZtRecipientEmail(e.target.value)}
              placeholder="someone@example.com"
            />
            <Select
              label="Max views"
              value={ztMaxViews}
              onValueChange={setZtMaxViews}
              options={[
                { value: '0',  label: 'Unlimited' },
                { value: '1',  label: '1 view' },
                { value: '5',  label: '5 views' },
                { value: '25', label: '25 views' },
              ]}
            />
          </>
        )}

        {mode !== 'people' && (
          <>
            <Select label="Expires" value={expiryHours} onValueChange={setExpiryHours} options={expiryOptions} />
            {shareUrl ? (
              // min-w-0 + overflow-hidden on the row: the URL is one
              // unbreakable token, and a flex child defaults to
              // min-width:auto — without the override the row grows past
              // the dialog panel instead of truncating (the "torn
              // dialog" bug).
              <div
                data-testid="share-link-result"
                className="flex min-w-0 items-center gap-2 overflow-hidden rounded-xl bg-muted p-2 shadow-neu-inset"
              >
                <Link2 className="h-4 w-4 shrink-0 text-[var(--color-primary)]" />
                <code className="min-w-0 flex-1 truncate text-xs" title={shareUrl}>{shareUrl}</code>
                <Button variant="ghost" size="sm" className="shrink-0" onClick={copyLink} aria-label="Copy share link"><Copy className="h-4 w-4" /></Button>
              </div>
            ) : (
              <Button onClick={handleCreate} disabled={creating} className="w-full">
                {creating ? 'Creating…' : 'Create Share Link'}
              </Button>
            )}
          </>
        )}
      </div>
    </Dialog>
  )
}
