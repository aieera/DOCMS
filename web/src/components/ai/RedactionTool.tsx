import { useState } from 'react'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Shield, Check } from 'lucide-react'
import * as Checkbox from '@radix-ui/react-checkbox'
import { detectRedactions } from '@/api/intelligence'

interface Entity { entity_type: string; entity_value: string; start_offset: number; end_offset: number; is_pii: boolean }
interface Props { open: boolean; onOpenChange: (o: boolean) => void; documentId: string; versionId: string; text: string }

export function RedactionTool({ open, onOpenChange, documentId, versionId, text }: Props) {
  const [entities, setEntities] = useState<Entity[]>([])
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [loading, setLoading] = useState(false)
  const [detected, setDetected] = useState(false)

  const detect = async () => {
    setLoading(true)
    try {
      const resp = await detectRedactions(documentId, versionId, text)
      const ents = resp.candidates || []
      setEntities(ents)
      setSelected(new Set(ents.map((_: Entity, i: number) => i)))
      setDetected(true)
    } catch { /* ignore */ }
    setLoading(false)
  }

  const toggle = (i: number) => {
    setSelected((prev) => { const next = new Set(prev); next.has(i) ? next.delete(i) : next.add(i); return next })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Redact PII" size="lg">
      {!detected ? (
        <div className="flex flex-col items-center gap-3 py-8">
          <Shield className="h-10 w-10 text-[var(--color-primary)]" />
          <p className="text-sm text-[var(--color-text-secondary)]">Detect personally identifiable information</p>
          <Button onClick={detect} loading={loading}>Detect PII</Button>
        </div>
      ) : (
        <div className="space-y-3">
          <p className="text-sm">{entities.length} entities found — select which to redact:</p>
          <div className="max-h-80 space-y-1 overflow-y-auto">
            {entities.map((e, i) => (
              <label key={i} className="flex cursor-pointer items-center gap-2 rounded-md p-2 hover:bg-slate-50 dark:hover:bg-slate-800">
                <Checkbox.Root checked={selected.has(i)} onCheckedChange={() => toggle(i)} className="flex h-4 w-4 items-center justify-center rounded border border-[var(--color-border)] data-[state=checked]:bg-[var(--color-primary)] data-[state=checked]:border-[var(--color-primary)]">
                  <Checkbox.Indicator><Check className="h-3 w-3 text-white" /></Checkbox.Indicator>
                </Checkbox.Root>
                <span className="rounded bg-red-100 px-1.5 py-0.5 text-xs font-medium text-red-800 dark:bg-red-900 dark:text-red-200">{e.entity_type}</span>
                <code className="text-sm">{e.entity_value}</code>
              </label>
            ))}
          </div>
          <div className="flex justify-end gap-2 pt-3">
            <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button variant="destructive" disabled={selected.size === 0}>Redact {selected.size} entities</Button>
          </div>
        </div>
      )}
    </Dialog>
  )
}
