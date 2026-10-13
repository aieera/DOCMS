import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { createFileRoute, redirect } from '@tanstack/react-router'
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

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/protection?tab=watermark). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/tenant/watermark')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/protection', search: { tab: 'watermark' }, replace: true })
  },
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
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const cfgQ = useQuery({ queryKey: ['admin', 'watermark', 'config'], queryFn: getWatermarkConfig })
  const overridesQ = useQuery({
    queryKey: ['admin', 'watermark', 'overrides'],
    queryFn: listWatermarkOverrides,
  })

  const invalidate = (k: string) =>
    qc.invalidateQueries({ queryKey: ['admin', 'watermark', k] })

  return (
    // See classification.tsx: the old `mx-auto max-w-4xl p-6` centred
    // 896px of content in a 1596px panel and double-padded it.
    <div className="space-y-6">
      <PageHeader
        noMargin
        title={t('protection.wm.title')}
        description={t('protection.wm.description')}
      />

      {/* The tenant default on the left, per-classification overrides and
          the list they produce on the right -- builder above its own
          results, same pairing as the classification rules. */}
      <div className="grid gap-6 xl:grid-cols-[minmax(340px,440px)_minmax(0,1fr)]">
        <div className="space-y-6">
          {cfgQ.isLoading ? (
            <Spinner />
          ) : (
            cfgQ.data && <ConfigCard cfg={cfgQ.data} onDone={() => invalidate('config')} />
          )}
        </div>

        <div className="min-w-0 space-y-6">
          <OverrideBuilderCard onDone={() => invalidate('overrides')} />
          {overridesQ.isLoading ? (
            <Spinner />
          ) : (
            <OverridesTable overrides={overridesQ.data ?? []} onDone={() => invalidate('overrides')} />
          )}
        </div>
      </div>
    </div>
  )
}

function ConfigCard({ cfg, onDone }: { cfg: WatermarkConfig; onDone: () => void }) {
  const { t } = useTranslation('common')
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
      toast.success(t('protection.wm.saved'))
      onDone()
    },
    onError: (e: unknown) => toast.error(`Save failed: ${(e as Error).message}`),
  })

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Stamp className="h-4 w-4" /> {t('protection.wm.config')}
      </div>

      {/* items-start + one span: see classification.tsx. Bare text beside
          a checkbox in a flex row wraps as flex items, not as a sentence,
          which shows up as soon as the card sits in a narrow column. */}
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          className="mt-0.5 shrink-0"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        <span>{t('protection.wm.enable')}</span>
      </label>

      <div className="mt-4">
        <Input
          label={t('protection.wm.template')}
          value={template}
          onChange={(e) => setTemplate(e.target.value)}
          placeholder={t('protection.wm.template_placeholder')}
        />
        <p className="mt-1.5 text-xs text-muted-foreground">
          {t('protection.wm.tokens')}{' '}
          {WATERMARK_TOKENS.map((tok, i) => (
            <span key={tok}>
              {i > 0 && ' '}
              <code className="rounded bg-muted px-1 py-0.5 font-mono" dir="ltr">{tok}</code>
            </span>
          ))}
        </p>
      </div>

      <div className="mt-3 rounded-md border border-border bg-muted/30 p-3">
        <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {t('protection.wm.preview')}
        </p>
        <p className="mt-1 break-words font-mono text-sm" data-testid="watermark-preview">
          {renderTemplate(template) || '—'}
        </p>
      </div>

      <div className="mt-4 grid gap-4 sm:grid-cols-2">
        <Input
          label={t('protection.wm.opacity')}
          type="number"
          min={0}
          max={100}
          value={String(opacity)}
          onChange={(e) => setOpacity(clampNumber(e.target.value, 0, 100, opacity))}
        />
        <Input
          label={t('protection.wm.rotation')}
          type="number"
          min={-360}
          max={360}
          value={String(rotation)}
          onChange={(e) => setRotation(numberOr(e.target.value, rotation))}
        />
        <Input
          label={t('protection.wm.font_size')}
          type="number"
          min={1}
          value={String(fontSize)}
          onChange={(e) => setFontSize(numberOr(e.target.value, fontSize))}
        />
        <div className="space-y-1.5">
          <label className="block text-sm font-medium" htmlFor="watermark-color">
            {t('protection.wm.color')}
          </label>
          <div className="flex items-center gap-2">
            <input
              id="watermark-color"
              type="color"
              value={normalizeHex(color)}
              onChange={(e) => setColor(e.target.value)}
              className="h-9 w-12 cursor-pointer rounded-md border border-input bg-background"
              aria-label={t('protection.wm.color_aria')}
            />
            <Input
              value={color}
              onChange={(e) => setColor(e.target.value)}
              placeholder={t('protection.wm.color_placeholder')}
              className="font-mono"
            />
          </div>
        </div>
      </div>

      <label className="mt-4 flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          className="mt-0.5 shrink-0"
          checked={tile}
          onChange={(e) => setTile(e.target.checked)}
        />
        <span>{t('protection.wm.tile_note')}</span>
      </label>

      <div className="mt-4">
        <Button onClick={() => save.mutate()} disabled={save.isPending}>
          {save.isPending ? t('protection.wm.saving') : t('protection.wm.save_settings')}
        </Button>
      </div>
    </Card>
  )
}

function OverrideBuilderCard({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation('common')
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
      toast.success(t('protection.wm.override_added'))
      setDescription('')
      setOpacityStr('')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Add failed: ${(e as Error).message}`),
  })

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Plus className="h-4 w-4" /> {t('protection.wm.add_override')}
      </div>
      <p className="mb-3 text-sm text-muted-foreground">
        <Trans
          i18nKey="protection.wm.override_hint"
          ns="common"
          components={{
            1: <code className="rounded bg-muted px-1" dir="ltr" />,
            2: <code className="rounded bg-muted px-1" dir="ltr" />,
          }}
        />
      </p>

      <div className="grid gap-4 sm:grid-cols-3">
        <LabeledSelect
          label={t('protection.wm.classification')}
          value={classification}
          onValueChange={(v) => setClassification(v as WatermarkClassification)}
          options={CLASSIFICATION_OPTS}
        />
        <LabeledSelect
          label={t('protection.wm.tile')}
          value={tileMode}
          onValueChange={(v) => setTileMode(v as 'inherit' | 'on' | 'off')}
          options={[
            { value: 'inherit', label: t('protection.wm.inherit_default') },
            { value: 'on', label: t('protection.wm.tiled') },
            { value: 'off', label: t('protection.wm.single_mark') },
          ]}
        />
        <Input
          label={t('protection.wm.opacity_inherit')}
          type="number"
          min={0}
          max={100}
          value={opacityStr}
          onChange={(e) => setOpacityStr(e.target.value)}
          placeholder={t('protection.wm.inherit')}
        />
      </div>

      <div className="mt-3 flex flex-wrap gap-x-6 gap-y-2">
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          {t('protection.wm.enabled')}
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />
          {t('protection.wm.force_note')}
        </label>
      </div>

      <div className="mt-3">
        <Input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={t('protection.wm.desc_placeholder')}
        />
      </div>

      <div className="mt-4">
        <Button onClick={() => create.mutate()} disabled={create.isPending}>
          {create.isPending ? t('protection.wm.adding') : t('protection.wm.add_override')}
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
  const { t } = useTranslation('common')
  const del = useAppMutation({
    mutationFn: (id: string) => deleteWatermarkOverride(id),
    onSuccess: () => {
      toast.success(t('protection.wm.override_removed'))
      onDone()
    },
    onError: (e: unknown) => toast.error(`Remove failed: ${(e as Error).message}`),
  })

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Droplet className="h-4 w-4" /> {t('protection.wm.overrides')}
      </div>
      {overrides.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t('protection.wm.no_overrides')}
        </p>
      ) : (
        <div className="overflow-hidden rounded-md border border-border">
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-start text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-3 py-2">Classification</th>
                <th className="px-3 py-2">{t('protection.wm.enabled')}</th>
                <th className="px-3 py-2">{t('protection.wm.col_opacity')}</th>
                <th className="px-3 py-2">Tile</th>
                <th className="px-3 py-2">{t('protection.wm.force')}</th>
                <th className="px-3 py-2">{t('protection.wm.col_description')}</th>
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
                      <Trash2 className="h-3.5 w-3.5" /> {t('protection.wm.remove')}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
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
