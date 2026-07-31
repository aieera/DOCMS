import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Stamp, Trash2, Plus, Droplet } from 'lucide-react'
import { toast } from 'sonner'

import { useAppMutation } from '@/hooks/useAppMutation'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect } from '@/components/ui/shadcn/select'
import { Card } from '@/components/ui/card'
import { Spinner } from '@/components/ui/Spinner'
import {
  getWatermarkConfig,
  setWatermarkConfig,
  listWatermarkOverrides,
  createWatermarkOverride,
  deleteWatermarkOverride,
  WATERMARK_CLASSIFICATIONS,
  WATERMARK_TOKENS,
  type WatermarkConfig,
  type WatermarkOverride,
  type WatermarkClassification,
} from '@/api/watermark'

// /admin/tenant/watermark — dynamic viewer watermark. The document
// service renders a per-viewer watermark (email · timestamp · ip · …)
// into the preview raster + print/download PDF so leaked screenshots
// trace back to a viewer. This page configures the tenant-wide template
// + look, plus per-classification overrides (e.g. force a restricted
// watermark that can't be disabled).

export const Route = createFileRoute('/_authenticated/admin/tenant/watermark')({
  component: WatermarkPage,
})

const CLASSIFICATION_OPTS = WATERMARK_CLASSIFICATIONS.map((c) => ({ value: c, label: c }))

// Sample token values for the live preview so the admin sees roughly
// what a viewer will get without opening a document.
const SAMPLE_TOKENS: Record<string, string> = {
  '{email}': 'alice@acme.com',
  '{timestamp}': '2026-07-01 14:03 UTC',
  '{ip}': '203.0.113.7',
  '{tenant}': 'Acme Corp',
  '{classification}': 'confidential',
  '{user_id}': 'usr_8f2a',
}

function renderTemplate(template: string): string {
  let out = template
  for (const [token, value] of Object.entries(SAMPLE_TOKENS)) {
    out = out.split(token).join(value)
  }
  return out
}

export function WatermarkPage() {
  const qc = useQueryClient()
  const cfgQ = useQuery({ queryKey: ['admin', 'watermark', 'config'], queryFn: getWatermarkConfig })
  const overridesQ = useQuery({
    queryKey: ['admin', 'watermark', 'overrides'],
    queryFn: listWatermarkOverrides,
  })

  const invalidate = (k: string) =>
    qc.invalidateQueries({ queryKey: ['admin', 'watermark', k] })

  return (
    <div className="mx-auto max-w-4xl p-6">
      <PageHeader
        title="Dynamic viewer watermark"
        description="Burn a per-viewer watermark (email, timestamp, IP, …) into document previews, print, and download so leaks are traceable."
      />

      {cfgQ.isLoading ? (
        <Spinner />
      ) : (
        cfgQ.data && <ConfigCard cfg={cfgQ.data} onDone={() => invalidate('config')} />
      )}

      <OverrideBuilderCard onDone={() => invalidate('overrides')} />

      {overridesQ.isLoading ? (
        <Spinner />
      ) : (
        <OverridesTable overrides={overridesQ.data ?? []} onDone={() => invalidate('overrides')} />
      )}
    </div>
  )
}

function ConfigCard({ cfg, onDone }: { cfg: WatermarkConfig; onDone: () => void }) {
  const [enabled, setEnabled] = useState(cfg.enabled)
  const [template, setTemplate] = useState(cfg.template)
  const [opacity, setOpacity] = useState(cfg.opacity)
  const [rotation, setRotation] = useState(cfg.rotation_deg)
  const [tile, setTile] = useState(cfg.tile)
  const [fontSize, setFontSize] = useState(cfg.font_size)
  const [color, setColor] = useState(cfg.color)

  const save = useAppMutation({
    mutationFn: () =>
      setWatermarkConfig({
        enabled,
        template,
        opacity,
        rotation_deg: rotation,
        tile,
        font_size: fontSize,
        color,
      }),
    onSuccess: () => {
      toast.success('Watermark settings saved')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Save failed: ${(e as Error).message}`),
  })

  return (
    <Card className="mb-6 p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Stamp className="h-4 w-4" /> Watermark configuration
      </div>

      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        Enable the dynamic viewer watermark for this tenant
      </label>

      <div className="mt-4">
        <Input
          label="Template"
          value={template}
          onChange={(e) => setTemplate(e.target.value)}
          placeholder="{email} · {timestamp} · {ip}"
        />
        <p className="mt-1.5 text-xs text-muted-foreground">
          Tokens:{' '}
          {WATERMARK_TOKENS.map((t, i) => (
            <span key={t}>
              {i > 0 && ' '}
              <code className="rounded bg-muted px-1 py-0.5 font-mono">{t}</code>
            </span>
          ))}
        </p>
      </div>

      <div className="mt-3 rounded-md border border-border bg-muted/30 p-3">
        <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Live preview
        </p>
        <p className="mt-1 break-words font-mono text-sm" data-testid="watermark-preview">
          {renderTemplate(template) || '—'}
        </p>
      </div>

      <div className="mt-4 grid gap-4 sm:grid-cols-2">
        <Input
          label="Opacity (0–100)"
          type="number"
          min={0}
          max={100}
          value={String(opacity)}
          onChange={(e) => setOpacity(clampNumber(e.target.value, 0, 100, opacity))}
        />
        <Input
          label="Rotation (degrees)"
          type="number"
          min={-360}
          max={360}
          value={String(rotation)}
          onChange={(e) => setRotation(numberOr(e.target.value, rotation))}
        />
        <Input
          label="Font size"
          type="number"
          min={1}
          value={String(fontSize)}
          onChange={(e) => setFontSize(numberOr(e.target.value, fontSize))}
        />
        <div className="space-y-1.5">
          <label className="block text-sm font-medium" htmlFor="watermark-color">
            Color
          </label>
          <div className="flex items-center gap-2">
            <input
              id="watermark-color"
              type="color"
              value={normalizeHex(color)}
              onChange={(e) => setColor(e.target.value)}
              className="h-9 w-12 cursor-pointer rounded-md border border-input bg-background"
              aria-label="Watermark color"
            />
            <Input
              value={color}
              onChange={(e) => setColor(e.target.value)}
              placeholder="#888888"
              className="font-mono"
            />
          </div>
        </div>
      </div>

      <label className="mt-4 flex items-center gap-2 text-sm">
        <input type="checkbox" checked={tile} onChange={(e) => setTile(e.target.checked)} />
        Tile — repeat the watermark across the whole page (vs. a single centered mark)
      </label>

      <div className="mt-4">
        <Button onClick={() => save.mutate()} disabled={save.isPending}>
          {save.isPending ? 'Saving…' : 'Save watermark settings'}
        </Button>
      </div>
    </Card>
  )
}

function OverrideBuilderCard({ onDone }: { onDone: () => void }) {
  const [classification, setClassification] = useState<WatermarkClassification>('confidential')
  const [enabled, setEnabled] = useState(true)
  const [opacityStr, setOpacityStr] = useState('')
  const [tileMode, setTileMode] = useState<'inherit' | 'on' | 'off'>('inherit')
  const [force, setForce] = useState(false)
  const [description, setDescription] = useState('')

  const create = useAppMutation({
    mutationFn: () => {
      const opacity = opacityStr.trim() === '' ? null : clampNumber(opacityStr, 0, 100, 0)
      const tile = tileMode === 'inherit' ? null : tileMode === 'on'
      return createWatermarkOverride({
        classification,
        enabled,
        opacity,
        tile,
        force,
        description,
      })
    },
    onSuccess: () => {
      toast.success('Override added')
      setDescription('')
      setOpacityStr('')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Add failed: ${(e as Error).message}`),
  })

  return (
    <Card className="mb-6 p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Plus className="h-4 w-4" /> Add per-classification override
      </div>
      <p className="mb-3 text-sm text-muted-foreground">
        Override the tenant default for documents of a given classification — e.g. force a stronger,
        non-dismissible watermark on <code className="rounded bg-muted px-1">restricted</code> or{' '}
        <code className="rounded bg-muted px-1">phi</code> documents.
      </p>

      <div className="grid gap-4 sm:grid-cols-3">
        <LabeledSelect
          label="Classification"
          value={classification}
          onValueChange={(v) => setClassification(v as WatermarkClassification)}
          options={CLASSIFICATION_OPTS}
        />
        <LabeledSelect
          label="Tile"
          value={tileMode}
          onValueChange={(v) => setTileMode(v as 'inherit' | 'on' | 'off')}
          options={[
            { value: 'inherit', label: 'inherit default' },
            { value: 'on', label: 'tiled' },
            { value: 'off', label: 'single mark' },
          ]}
        />
        <Input
          label="Opacity (blank = inherit)"
          type="number"
          min={0}
          max={100}
          value={opacityStr}
          onChange={(e) => setOpacityStr(e.target.value)}
          placeholder="inherit"
        />
      </div>

      <div className="mt-3 flex flex-wrap gap-x-6 gap-y-2">
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Enabled
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />
          Force — viewers can&rsquo;t suppress this watermark
        </label>
      </div>

      <div className="mt-3">
        <Input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="Description (optional)"
        />
      </div>

      <div className="mt-4">
        <Button onClick={() => create.mutate()} disabled={create.isPending}>
          {create.isPending ? 'Adding…' : 'Add override'}
        </Button>
      </div>
    </Card>
  )
}

function OverridesTable({
  overrides,
  onDone,
}: {
  overrides: WatermarkOverride[]
  onDone: () => void
}) {
  const del = useAppMutation({
    mutationFn: (id: string) => deleteWatermarkOverride(id),
    onSuccess: () => {
      toast.success('Override removed')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Remove failed: ${(e as Error).message}`),
  })

  return (
    <Card className="mb-6 p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Droplet className="h-4 w-4" /> Overrides
      </div>
      {overrides.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No overrides. Every document uses the tenant default above.
        </p>
      ) : (
        <div className="overflow-hidden rounded-md border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-start text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-3 py-2">Classification</th>
                <th className="px-3 py-2">Enabled</th>
                <th className="px-3 py-2">Opacity</th>
                <th className="px-3 py-2">Tile</th>
                <th className="px-3 py-2">Force</th>
                <th className="px-3 py-2">Description</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {overrides.map((o) => (
                <tr key={o.id}>
                  <td className="px-3 py-2 font-mono">{o.classification}</td>
                  <td className="px-3 py-2">{o.enabled ? 'yes' : 'no'}</td>
                  <td className="px-3 py-2">{o.opacity == null ? '—' : `${o.opacity}%`}</td>
                  <td className="px-3 py-2">{o.tile == null ? '—' : o.tile ? 'tiled' : 'single'}</td>
                  <td className="px-3 py-2">{o.force ? 'forced' : '—'}</td>
                  <td className="px-3 py-2 text-muted-foreground">{o.description || '—'}</td>
                  <td className="px-3 py-2 text-end">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="gap-1 text-red-600 hover:text-red-700"
                      onClick={() => del.mutate(o.id)}
                      disabled={del.isPending}
                    >
                      <Trash2 className="h-3.5 w-3.5" /> Remove
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}

// ---- helpers ------------------------------------------------------------

function numberOr(raw: string, fallback: number): number {
  const n = Number(raw)
  return raw.trim() === '' || Number.isNaN(n) ? fallback : n
}

function clampNumber(raw: string, min: number, max: number, fallback: number): number {
  const n = numberOr(raw, fallback)
  return Math.min(Math.max(n, min), max)
}

// The native <input type="color"> only accepts #rrggbb. Coerce shorthand
// / malformed values to a safe default so the swatch never goes blank.
function normalizeHex(hex: string): string {
  const v = hex.trim()
  if (/^#[0-9a-fA-F]{6}$/.test(v)) return v
  if (/^#[0-9a-fA-F]{3}$/.test(v)) {
    return '#' + v.slice(1).split('').map((c) => c + c).join('')
  }
  return '#888888'
}
