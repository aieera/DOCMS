// Admin page: per-tenant upload format allowlist (migration 000060).
// Owner|admin only. Empty lists = no allowlist enforced (executable
// blocklist still applies). The storage service reads the same table
// on InitiateUpload, so what's saved here gates uploads org-wide.
import { useEffect, useState } from 'react'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { Plus, X } from 'lucide-react'
import { toast } from 'sonner'

import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Card } from '@/components/ui/card'
import { getUploadPolicy, setUploadPolicy } from '@/api/uploadPolicy'

// Suggestion presets — admin clicks a preset to seed the chips, then
// can prune. These mirror common DMS scopes; not enforced, just a
// shortcut so the most common cases don't require typing every entry.
const PRESETS: { label: string; mimes: string[]; exts: string[] }[] = [
  {
    label: 'Documents (PDF + Office)',
    mimes: [
      'application/pdf',
      'application/msword',
      'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
      'application/vnd.ms-excel',
      'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      'application/vnd.ms-powerpoint',
      'application/vnd.openxmlformats-officedocument.presentationml.presentation',
    ],
    exts: ['.pdf', '.doc', '.docx', '.xls', '.xlsx', '.ppt', '.pptx'],
  },
  {
    label: 'Images',
    mimes: ['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/tiff'],
    exts: ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.tif', '.tiff'],
  },
  {
    label: 'Web (HTML + text)',
    mimes: ['text/html', 'text/plain', 'text/markdown'],
    exts: ['.html', '.htm', '.txt', '.md'],
  },
]

export function UploadPolicyPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'upload-policy'],
    queryFn: getUploadPolicy,
  })

  // Local edit buffer; only persists on Save. Re-sync when the server
  // payload arrives. We keep both lists as arrays to preserve admin
  // ordering — Set would lose it.
  const [mimes, setMimes] = useState<string[]>([])
  const [exts, setExts] = useState<string[]>([])
  const [mimeDraft, setMimeDraft] = useState('')
  const [extDraft, setExtDraft] = useState('')

  useEffect(() => {
    if (data) {
      setMimes(data.allowed_mime_types)
      setExts(data.allowed_extensions)
    }
  }, [data])

  const save = useAppMutation({
    mutationFn: () =>
      setUploadPolicy({ allowed_mime_types: mimes, allowed_extensions: exts }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'upload-policy'] })
      toast.success('Upload policy saved')
    },
    onError: (e: unknown) => {
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
        ?? (e as Error).message
      toast.error(`Save failed: ${msg}`)
    },
  })

  const addMime = () => {
    const v = mimeDraft.trim()
    if (!v || mimes.includes(v)) return
    setMimes([...mimes, v])
    setMimeDraft('')
  }
  const addExt = () => {
    let v = extDraft.trim().toLowerCase()
    if (!v) return
    if (!v.startsWith('.')) v = '.' + v
    if (exts.includes(v)) return
    setExts([...exts, v])
    setExtDraft('')
  }
  const applyPreset = (i: number) => {
    const p = PRESETS[i]
    const mimeMerged = Array.from(new Set([...mimes, ...p.mimes]))
    const extMerged = Array.from(new Set([...exts, ...p.exts]))
    setMimes(mimeMerged)
    setExts(extMerged)
  }
  const clearAll = () => {
    setMimes([])
    setExts([])
  }

  const allowlistEmpty = mimes.length === 0 && exts.length === 0

  return (
    <div className="space-y-6">
      <PageHeader
        title="Upload policy"
        description="Restrict which file formats users can upload across the entire organization. Leave both lists empty to allow any non-executable file."
        actions={
          <>
            <Button
              variant="outline"
              onClick={clearAll}
              disabled={isLoading || save.isPending}
              className="border-destructive/30 text-destructive hover:bg-destructive/10 hover:text-destructive"
            >
              Clear all
            </Button>
            <Button
              onClick={() => save.mutate()}
              disabled={isLoading || save.isPending}
              loading={save.isPending}
              className="gap-2 shadow-sm hover:shadow-md"
            >
              Save policy
            </Button>
          </>
        }
      />

      {allowlistEmpty && !isLoading && (
        <Card className="flex items-start gap-3 border-dashed bg-muted/30 p-4 text-sm">
          <div>
            <p className="font-medium">No allowlist set</p>
            <p className="mt-1 text-xs text-muted-foreground">
              Any file type is allowed (except the built-in executable blocklist:
              .exe, .bat, .sh, .ps1, etc.). Pick a preset below or add MIME types /
              extensions manually.
            </p>
          </div>
        </Card>
      )}

      <Card className="p-4">
        <h2 className="mb-2 text-sm font-semibold">Quick presets</h2>
        <p className="mb-3 text-xs text-muted-foreground">
          Append a preset to the current lists. You can prune individual entries afterwards.
        </p>
        <div className="flex flex-wrap gap-2">
          {PRESETS.map((p, i) => (
            <Button key={p.label} variant="outline" size="sm" onClick={() => applyPreset(i)}>
              {p.label}
            </Button>
          ))}
        </div>
      </Card>

      <ListSection
        title="Allowed MIME types"
        hint="Exact match (case-insensitive). e.g. application/pdf"
        items={mimes}
        onRemove={(i) => setMimes(mimes.filter((_, idx) => idx !== i))}
        draft={mimeDraft}
        onDraftChange={setMimeDraft}
        onAdd={addMime}
        placeholder="application/pdf"
      />
      <ListSection
        title="Allowed file extensions"
        hint="With or without leading dot — normalised to lowercase .ext. e.g. .pdf or pdf"
        items={exts}
        onRemove={(i) => setExts(exts.filter((_, idx) => idx !== i))}
        draft={extDraft}
        onDraftChange={setExtDraft}
        onAdd={addExt}
        placeholder=".pdf"
      />
    </div>
  )
}

function ListSection({
  title, hint, items, onRemove, draft, onDraftChange, onAdd, placeholder,
}: {
  title: string
  hint: string
  items: string[]
  onRemove: (i: number) => void
  draft: string
  onDraftChange: (v: string) => void
  onAdd: () => void
  placeholder: string
}) {
  return (
    <Card className="p-4">
      <h2 className="text-sm font-semibold">{title}</h2>
      <p className="mt-1 text-xs text-muted-foreground">{hint}</p>

      <div className="mt-3 flex flex-wrap gap-1.5">
        {items.length === 0 ? (
          <span className="text-xs italic text-muted-foreground">No entries.</span>
        ) : (
          items.map((v, i) => (
            <span
              key={v}
              className="inline-flex items-center gap-1 rounded-full border border-border bg-muted py-1 ps-2.5 pe-1 text-xs"
            >
              <code className="font-mono">{v}</code>
              <button
                type="button"
                onClick={() => onRemove(i)}
                className="inline-flex h-5 w-5 items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
                aria-label={`Remove ${v}`}
                title={`Remove ${v}`}
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </span>
          ))
        )}
      </div>

      <form
        className="mt-3 flex gap-2"
        onSubmit={(e) => { e.preventDefault(); onAdd() }}
      >
        <Input
          value={draft}
          onChange={(e) => onDraftChange(e.target.value)}
          placeholder={placeholder}
          className="flex-1"
        />
        <Button type="submit" variant="outline" size="sm" className="gap-1.5">
          <Plus className="h-3.5 w-3.5" /> Add
        </Button>
      </form>
    </Card>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/tenant-settings?tab=upload). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/tenant/upload-policy')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/tenant-settings', search: { tab: 'upload' }, replace: true })
  },
})
