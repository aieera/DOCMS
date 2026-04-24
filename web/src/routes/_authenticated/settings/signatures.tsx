import { useRef, useState, useEffect } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { PenLine, Star, Trash2, Upload as UploadIcon } from 'lucide-react'

import {
  createProfile,
  deleteProfile,
  listProfiles,
  profileImageURL,
  setDefaultProfile,
  type SignatureKind,
  type SignatureProfile,
} from '@/api/signatureProfiles'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Skeleton } from '@/components/ui/Skeleton'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/Badge'

function SignatureSettingsPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['signature-profiles'],
    queryFn: listProfiles,
  })

  const setDefault = useMutation({
    mutationFn: (id: string) => setDefaultProfile(id),
    onSuccess: () => {
      toast.success('Default updated')
      qc.invalidateQueries({ queryKey: ['signature-profiles'] })
    },
    onError: () => toast.error('Could not set default'),
  })
  const del = useMutation({
    mutationFn: (id: string) => deleteProfile(id),
    onSuccess: () => {
      toast.success('Profile deleted')
      qc.invalidateQueries({ queryKey: ['signature-profiles'] })
    },
    onError: () => toast.error('Delete failed'),
  })

  return (
    <div>
      <PageHeader
        title="Saved signatures"
        description="Reuse your signature on every PAdES envelope without redrawing."
      />

      <NewProfileCard
        onCreated={() => qc.invalidateQueries({ queryKey: ['signature-profiles'] })}
      />

      <h3 className="mt-6 mb-2 text-sm font-medium uppercase tracking-wide text-[var(--color-text-secondary)]">
        Your profiles
      </h3>

      {isLoading && (
        <div role="status" aria-live="polite" className="space-y-2">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      )}

      {!isLoading && (!data || data.length === 0) && (
        <EmptyState
          icon={<PenLine className="h-10 w-10" />}
          title="No saved signatures"
          description="Create one above to reuse when signing."
        />
      )}

      {!isLoading && data && data.length > 0 && (
        <ul className="space-y-3" aria-label="Saved signature profiles">
          {data.map((p) => (
            <ProfileRow
              key={p.id}
              profile={p}
              onSetDefault={() => setDefault.mutate(p.id)}
              onDelete={() => {
                if (confirm(`Delete signature "${p.name}"? This cannot be undone.`)) {
                  del.mutate(p.id)
                }
              }}
              busy={setDefault.isPending || del.isPending}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

function ProfileRow({
  profile,
  onSetDefault,
  onDelete,
  busy,
}: {
  profile: SignatureProfile
  onSetDefault: () => void
  onDelete: () => void
  busy: boolean
}) {
  return (
    <li className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
      <div className="flex items-center gap-4">
        <img
          src={profileImageURL(profile.id)}
          alt={`Signature for ${profile.name}`}
          className="h-16 w-40 rounded-md border border-[var(--color-border)] bg-white object-contain p-1"
        />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h4 className="truncate text-base font-medium">{profile.name}</h4>
            <Badge variant="default">{profile.kind}</Badge>
            {profile.is_default && (
              <Badge variant="active">
                <Star className="mr-1 h-3 w-3" aria-hidden="true" />
                Default
              </Badge>
            )}
          </div>
          <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
            Created {new Date(profile.created_at).toLocaleDateString()}
          </p>
        </div>
        <div className="flex gap-2">
          {!profile.is_default && (
            <Button
              variant="ghost"
              onClick={onSetDefault}
              disabled={busy}
              aria-label={`Set ${profile.name} as default`}
            >
              Make default
            </Button>
          )}
          <Button
            variant="ghost"
            onClick={onDelete}
            disabled={busy}
            aria-label={`Delete ${profile.name}`}
          >
            <Trash2 className="h-4 w-4" aria-hidden="true" />
          </Button>
        </div>
      </div>
    </li>
  )
}

// ---- New-profile card -----------------------------------------------------

function NewProfileCard({ onCreated }: { onCreated: () => void }) {
  const [kind, setKind] = useState<SignatureKind>('draw')
  const [name, setName] = useState('My signature')
  const [setDefault, setSetDefault] = useState(false)
  const [typedText, setTypedText] = useState('')
  const [typedFont, setTypedFont] = useState('Satisfy, cursive')
  const [fileDataURL, setFileDataURL] = useState<string | null>(null)

  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const drawing = useRef(false)

  // Set up a white-bg canvas for the draw kind.
  useEffect(() => {
    if (kind !== 'draw') return
    const c = canvasRef.current
    if (!c) return
    const ctx = c.getContext('2d')
    if (!ctx) return
    ctx.fillStyle = '#ffffff'
    ctx.fillRect(0, 0, c.width, c.height)
    ctx.strokeStyle = '#111827'
    ctx.lineWidth = 2.5
    ctx.lineCap = 'round'
  }, [kind])

  const pointerDown = (e: React.PointerEvent<HTMLCanvasElement>) => {
    drawing.current = true
    const c = canvasRef.current
    if (!c) return
    const ctx = c.getContext('2d')
    if (!ctx) return
    const { x, y } = canvasCoord(c, e)
    ctx.beginPath()
    ctx.moveTo(x, y)
  }
  const pointerMove = (e: React.PointerEvent<HTMLCanvasElement>) => {
    if (!drawing.current) return
    const c = canvasRef.current
    if (!c) return
    const ctx = c.getContext('2d')
    if (!ctx) return
    const { x, y } = canvasCoord(c, e)
    ctx.lineTo(x, y)
    ctx.stroke()
  }
  const pointerUp = () => {
    drawing.current = false
  }

  const clearCanvas = () => {
    const c = canvasRef.current
    if (!c) return
    const ctx = c.getContext('2d')
    if (!ctx) return
    ctx.fillStyle = '#ffffff'
    ctx.fillRect(0, 0, c.width, c.height)
  }

  const create = useMutation({
    mutationFn: async () => {
      const base64 = await buildImageBase64({
        kind,
        canvas: canvasRef.current,
        typedText,
        typedFont,
        fileDataURL,
      })
      return createProfile({
        name: name.trim(),
        kind,
        font_style: kind === 'typed' ? typedFont : undefined,
        imageBase64: base64,
        setDefault,
      })
    },
    onSuccess: () => {
      toast.success('Signature saved')
      onCreated()
      setTypedText('')
      setFileDataURL(null)
      clearCanvas()
    },
    onError: (err: unknown) => {
      const msg =
        err && typeof err === 'object' && 'response' in err
          ? // eslint-disable-next-line @typescript-eslint/no-explicit-any
            (err as any).response?.data?.error?.message ?? 'Could not save'
          : 'Could not save'
      toast.error(msg)
    },
  })

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        create.mutate()
      }}
      className="space-y-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
      aria-label="Create signature profile"
    >
      <div className="grid grid-cols-2 gap-3">
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} required />
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium text-[var(--color-text-secondary)]">Type</span>
          <select
            value={kind}
            onChange={(e) => setKind(e.target.value as SignatureKind)}
            className="h-9 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2"
          >
            <option value="draw">Draw</option>
            <option value="upload">Upload</option>
            <option value="typed">Typed</option>
          </select>
        </label>
      </div>

      {kind === 'draw' && (
        <div>
          <label className="mb-1 block text-sm font-medium">Draw below</label>
          <canvas
            ref={canvasRef}
            width={480}
            height={160}
            onPointerDown={pointerDown}
            onPointerMove={pointerMove}
            onPointerUp={pointerUp}
            onPointerLeave={pointerUp}
            className="rounded-md border border-[var(--color-border)] bg-white"
            aria-label="Signature drawing canvas"
            role="img"
          />
          <button
            type="button"
            onClick={clearCanvas}
            className="mt-2 text-xs text-[var(--color-primary)] hover:underline"
          >
            Clear
          </button>
        </div>
      )}

      {kind === 'upload' && (
        <div>
          <label className="mb-1 block text-sm font-medium">Upload PNG / JPEG</label>
          <input
            type="file"
            accept="image/png,image/jpeg"
            onChange={async (e) => {
              const f = e.target.files?.[0]
              if (!f) return
              if (f.size > 1024 * 1024) {
                toast.error('Image larger than 1 MB — please shrink it.')
                return
              }
              const reader = new FileReader()
              reader.onload = () => setFileDataURL(String(reader.result))
              reader.readAsDataURL(f)
            }}
            className="block text-sm"
            aria-label="Upload signature image"
          />
          {fileDataURL && (
            <img
              src={fileDataURL}
              alt="Uploaded signature preview"
              className="mt-2 h-20 rounded-md border border-[var(--color-border)] bg-white p-1"
            />
          )}
        </div>
      )}

      {kind === 'typed' && (
        <div className="grid grid-cols-2 gap-3">
          <Input
            label="Typed signature text"
            placeholder="Jane Doe"
            value={typedText}
            onChange={(e) => setTypedText(e.target.value)}
            required
          />
          <label className="flex flex-col gap-1 text-sm">
            <span className="font-medium text-[var(--color-text-secondary)]">Font</span>
            <select
              value={typedFont}
              onChange={(e) => setTypedFont(e.target.value)}
              className="h-9 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2"
            >
              <option value="Satisfy, cursive">Satisfy</option>
              <option value="Dancing Script, cursive">Dancing Script</option>
              <option value="Great Vibes, cursive">Great Vibes</option>
              <option value="Homemade Apple, cursive">Homemade Apple</option>
              <option value="Pacifico, cursive">Pacifico</option>
            </select>
          </label>
        </div>
      )}

      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={setDefault}
          onChange={(e) => setSetDefault(e.target.checked)}
        />
        Make default signature
      </label>

      <div className="flex justify-end gap-2">
        <Button type="submit" loading={create.isPending}>
          <UploadIcon className="mr-1 h-4 w-4" aria-hidden="true" />
          Save signature
        </Button>
      </div>
    </form>
  )
}

// ---- helpers --------------------------------------------------------------

function canvasCoord(c: HTMLCanvasElement, e: React.PointerEvent<HTMLCanvasElement>) {
  const rect = c.getBoundingClientRect()
  return {
    x: ((e.clientX - rect.left) / rect.width) * c.width,
    y: ((e.clientY - rect.top) / rect.height) * c.height,
  }
}

async function buildImageBase64(args: {
  kind: SignatureKind
  canvas: HTMLCanvasElement | null
  typedText: string
  typedFont: string
  fileDataURL: string | null
}): Promise<string> {
  if (args.kind === 'draw') {
    if (!args.canvas) throw new Error('canvas missing')
    return pngDataURLToBase64(args.canvas.toDataURL('image/png'))
  }
  if (args.kind === 'upload') {
    if (!args.fileDataURL) throw new Error('upload missing')
    return pngDataURLToBase64(args.fileDataURL)
  }
  // typed → render via an offscreen canvas
  const c = document.createElement('canvas')
  c.width = 480
  c.height = 160
  const ctx = c.getContext('2d')!
  ctx.fillStyle = '#ffffff'
  ctx.fillRect(0, 0, c.width, c.height)
  ctx.fillStyle = '#111827'
  ctx.font = `48px ${args.typedFont}`
  ctx.textBaseline = 'middle'
  ctx.fillText(args.typedText, 20, c.height / 2)
  return pngDataURLToBase64(c.toDataURL('image/png'))
}

function pngDataURLToBase64(url: string): string {
  const i = url.indexOf('base64,')
  return i >= 0 ? url.slice(i + 'base64,'.length) : url
}

export const Route = createFileRoute('/_authenticated/settings/signatures')({
  component: SignatureSettingsPage,
})
