import { useState } from 'react'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Copy, Link2, ShieldAlert } from 'lucide-react'
import { toast } from 'sonner'
import { createShareLink } from '@/api/shareLinks'
import { createZTShare } from '@/api/ztShare'
import { getVersions } from '@/api/documents'

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

type ShareMode = 'classic' | 'zt'

export function ShareDialog({ open, onOpenChange, documentId, documentTitle }: Props) {
  const [expiryHours, setExpiryHours] = useState('168')
  const [password, setPassword] = useState('')
  const [permission, setPermission] = useState<keyof typeof permissionOptions>('view')
  const [shareUrl, setShareUrl] = useState('')
  const [creating, setCreating] = useState(false)
  // ADR 0098 — Zero-trust view-only is a separate flow (own backend
  // endpoint, recipient watermark, sender activity panel).
  const [mode, setMode] = useState<ShareMode>('classic')
  const [ztRecipientEmail, setZtRecipientEmail] = useState('')
  const [ztMaxViews, setZtMaxViews] = useState('0')

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
    } catch {
      toast.error('Failed to create share link')
    } finally {
      setCreating(false)
    }
  }

  const copyLink = () => {
    navigator.clipboard.writeText(shareUrl)
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
            { value: 'classic', label: 'Standard share link' },
            { value: 'zt', label: 'Zero-trust view-only (watermarked, audited)' },
          ]}
        />

        {mode === 'zt' && (
          <div className="rounded-md border border-amber-500/40 bg-amber-50/60 p-3 text-xs dark:bg-amber-950/20">
            <div className="flex items-start gap-2">
              <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-amber-700 dark:text-amber-300" />
              <div>
                <p className="font-semibold text-amber-900 dark:text-amber-100">
                  View-only mode
                </p>
                <p className="mt-1 text-amber-800 dark:text-amber-200">
                  Blocks copy, print, and download. Every page is watermarked with the
                  recipient's email so leaks can be traced. <strong>Does not prevent
                  screenshots taken with the recipient's device.</strong>
                </p>
              </div>
            </div>
          </div>
        )}

        {mode === 'classic' ? (
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
            <Input
              label="Password (optional)"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="Leave empty for no password"
            />
          </>
        ) : (
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

        <Select label="Expires" value={expiryHours} onValueChange={setExpiryHours} options={expiryOptions} />
        {shareUrl ? (
          <div
            data-testid="share-link-result"
            className="flex items-center gap-2 rounded-md border border-[var(--color-border)] bg-slate-50 p-2 dark:bg-slate-800"
          >
            <Link2 className="h-4 w-4 shrink-0 text-[var(--color-primary)]" />
            <code className="flex-1 truncate text-xs">{shareUrl}</code>
            <Button variant="ghost" size="sm" onClick={copyLink}><Copy className="h-4 w-4" /></Button>
          </div>
        ) : (
          <Button onClick={handleCreate} disabled={creating} className="w-full">
            {creating ? 'Creating…' : 'Create Share Link'}
          </Button>
        )}
      </div>
    </Dialog>
  )
}
