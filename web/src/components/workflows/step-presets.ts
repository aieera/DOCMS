import { CheckCircle2, GitFork, Bell, Workflow, PenLine } from 'lucide-react'
import type { ADR0073Step } from '@/api/workflows'

export type StepType = ADR0073Step['type']

export interface StepPreset {
  type: StepType
  // i18n key under 'editor.step.<type>' resolves the user-facing label.
  // We keep the raw type as a stable id so the dropdown / chain icon
  // tinting key doesn't break if a translator renames the label.
  defaults: Partial<ADR0073Step>
  icon: typeof CheckCircle2
  // Tailwind classes for the step's icon dot in StepChainList + the
  // pill in the picker. Aligns with the theme tokens (success /
  // warning / info / primary) so dark mode + cream-bg both work.
  toneCls: string
}

export const STEP_PRESETS: readonly StepPreset[] = [
  {
    type: 'approval',
    defaults: { sla_hours: 48, on_expire: 'escalate', allow_delegate: true },
    icon: CheckCircle2,
    toneCls: 'bg-success/15 text-success border-success/40',
  },
  {
    type: 'signature',
    defaults: { sla_hours: 72, on_expire: 'auto_reject' },
    icon: PenLine,
    toneCls: 'bg-info/15 text-info border-info/40',
  },
  {
    type: 'notification',
    defaults: {},
    icon: Bell,
    toneCls: 'bg-muted text-foreground border-border',
  },
  {
    type: 'conditional',
    defaults: { condition_rego: 'true', on_true: [], on_false: [] },
    icon: GitFork,
    toneCls: 'bg-warning/15 text-warning border-warning/40',
  },
  {
    type: 'parallel',
    defaults: { mode: 'require_all', sla_hours: 48, on_expire: 'auto_reject' },
    icon: Workflow,
    toneCls: 'bg-primary/15 text-primary border-primary/40',
  },
] as const

export function presetFor(type: StepType): StepPreset {
  return STEP_PRESETS.find((p) => p.type === type) ?? STEP_PRESETS[0]
}

// newStepFrom builds an ADR0073Step from a preset, generating a stable
// id from now() base36. Used by the editor's "Add step" dropdown.
export function newStepFrom(preset: StepPreset): ADR0073Step {
  const id = `step-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`
  return { id, type: preset.type, name: defaultLabel(preset.type), ...preset.defaults }
}

function defaultLabel(type: StepType): string {
  // English fallback — the editor immediately overwrites this via
  // i18n once the chip mounts; this is just the persisted default
  // so a save before any edit doesn't ship an empty string.
  return type.charAt(0).toUpperCase() + type.slice(1)
}
