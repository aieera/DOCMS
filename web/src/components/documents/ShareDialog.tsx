import { useState } from 'react'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Copy, Link2 } from 'lucide-react'
import { toast } from 'sonner'
import { createShareLink } from '@/api/shareLinks'

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

export function ShareDialog({ open, onOpenChange, documentId, documentTitle }: Props) {
  const [expiryHours, setExpiryHours] = useState('168')
  const [password, setPassword] = useState('')
  const [permission, setPermission] = useState<keyof typeof permissionOptions>('view')
  const [shareUrl, setShareUrl] = useState('')
  const [creating, setCreating] = useState(false)

  const handleCreate = async () => {
    setCreating(true)
    try {
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
