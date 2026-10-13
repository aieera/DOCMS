import { useTranslation } from 'react-i18next'
import { AlertTriangle, Trash2 } from 'lucide-react'

import { Input } from '@/components/ui/shadcn/input'
import { Button } from '@/components/ui/shadcn/button'
import { Badge } from '@/components/ui/shadcn/badge'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/shadcn/select'
import type { ADR0073Step } from '@/api/workflows'
import type { ValidationIssue } from './validate'

interface Props {
  step: ADR0073Step
  onChange: (patch: Partial<ADR0073Step>) => void
  onRemove: () => void
  issues: ValidationIssue[]
}

// StepConfigPanel renders the end-side pane of the template editor:
// label + step-type-specific fields + delete. Inline issue list at
// the bottom mirrors the chip's red dot in StepChainList. The Step
// ID is shown but read-only — changing it would break any conditional
// branch referencing it elsewhere in the chain.
export function StepConfigPanel({ step, onChange, onRemove, issues }: Props) {
  const { t } = useTranslation('workflows')
  return (
    <div className="space-y-3 text-sm" data-testid="step-config-panel">
      <div className="flex items-center justify-between">
        <Badge variant={badgeVariantFor(step.type)}>{t(`editor.step.${step.type}`)}</Badge>
        <Button
          variant="ghost"
          size="sm"
          onClick={onRemove}
          data-testid="step-config-remove"
          aria-label="Remove step"
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </div>

      <Field label={t('editor.fields.name')}>
        <Input value={step.name ?? ''} onChange={(e) => onChange({ name: e.target.value })} />
      </Field>

      <Field label={`${t('editor.fields.name')} ID`}>
        <Input value={step.id} readOnly className="font-mono text-xs" />
      </Field>

      {(step.type === 'approval' || step.type === 'signature') && (
        <>
          <Field label={t('editor.fields.assignee_type')}>
            <Select
              value={step.assignee?.type ?? 'user'}
              onValueChange={(v) =>
                onChange({
                  assignee: {
                    type: v as ADR0073Step['assignee'] extends infer A ? A extends { type: infer T } ? T : never : never,
                    value: step.assignee?.value ?? '',
                  } as ADR0073Step['assignee'],
                })
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">{t('editor.fields.assignee_user')}</SelectItem>
                <SelectItem value="group">{t('editor.fields.assignee_group')}</SelectItem>
                <SelectItem value="dynamic">{t('editor.fields.assignee_dynamic')}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('editor.fields.assignee_value')}
            help={t('editor.fields.assignee_value_help')}
          >
            <Input
              value={step.assignee?.value ?? ''}
              onChange={(e) =>
                onChange({
                  assignee: { type: step.assignee?.type ?? 'user', value: e.target.value },
                })
              }
            />
          </Field>
        </>
      )}

      {step.type === 'parallel' && (
        <>
          <Field label={t('editor.fields.join_mode')}>
            <Select
              value={step.mode ?? 'require_all'}
              onValueChange={(v) =>
                onChange({ mode: v as 'require_all' | 'require_any' })
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="require_all">{t('editor.fields.require_all')}</SelectItem>
                <SelectItem value="require_any">{t('editor.fields.require_any')}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('editor.fields.approvers')}
            help={t('editor.fields.approvers_help')}
          >
            <Input
              value={(step.approvers ?? []).join(',')}
              onChange={(e) =>
                onChange({
                  approvers: e.target.value
                    .split(',')
                    .map((s) => s.trim())
                    .filter(Boolean),
                })
              }
            />
          </Field>
        </>
      )}

      {step.type === 'conditional' && (
        <>
          <Field label={t('editor.fields.condition_rego')}>
            <Input
              value={step.condition_rego ?? ''}
              onChange={(e) => onChange({ condition_rego: e.target.value })}
              placeholder="input.document.custom_metadata.amount > 100000"
              className="font-mono text-xs"
            />
          </Field>
          <Field label={t('editor.fields.on_true')}>
            <Input
              value={(step.on_true ?? []).join(',')}
              onChange={(e) =>
                onChange({
                  on_true: e.target.value.split(',').map((s) => s.trim()).filter(Boolean),
                })
              }
              placeholder="step-abc, step-def"
              className="font-mono text-xs"
            />
          </Field>
          <Field label={t('editor.fields.on_false')}>
            <Input
              value={(step.on_false ?? []).join(',')}
              onChange={(e) =>
                onChange({
                  on_false: e.target.value.split(',').map((s) => s.trim()).filter(Boolean),
                })
              }
              placeholder="step-ghi"
              className="font-mono text-xs"
            />
          </Field>
        </>
      )}

      {(step.type === 'approval' || step.type === 'parallel' || step.type === 'signature') && (
        <>
          <Field label={t('editor.fields.sla_hours')}>
            <Input
              type="number"
              min={1}
              max={720}
              value={step.sla_hours ?? ''}
              onChange={(e) =>
                onChange({
                  sla_hours: e.target.value === '' ? undefined : Number(e.target.value),
                })
              }
            />
          </Field>
          <Field label={t('editor.fields.on_expire')}>
            <Select
              value={step.on_expire ?? 'escalate'}
              onValueChange={(v) =>
                onChange({ on_expire: v as 'escalate' | 'auto_approve' | 'auto_reject' })
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="escalate">Escalate</SelectItem>
                <SelectItem value="auto_approve">Auto-approve</SelectItem>
                <SelectItem value="auto_reject">Auto-reject</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </>
      )}

      {step.on_expire === 'escalate' && (
        <Field label={t('editor.fields.escalation')}>
          <Select
            value={step.escalation?.strategy ?? 'manager'}
            onValueChange={(v) =>
              onChange({
                escalation: { ...(step.escalation ?? {}), strategy: v as 'manager' | 'fixed' | 'chain' },
              })
            }
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="manager">Up the manager chain</SelectItem>
              <SelectItem value="fixed">Fixed user</SelectItem>
              <SelectItem value="chain">Explicit chain</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      )}

      {issues.length > 0 && (
        <ul
          className="space-y-1 rounded-md border border-warning/40 bg-warning/10 p-2 text-xs text-warning-strong"
          data-testid="step-config-issues"
        >
          {issues.map((i, k) => (
            <li key={k} className="flex items-start gap-1.5">
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
              <span>{i.message}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function Field({
  label,
  help,
  children,
}: {
  label: string
  help?: string
  children: React.ReactNode
}) {
  return (
    <div className="space-y-1">
      <label className="block text-xs font-medium text-muted-foreground">{label}</label>
      {children}
      {help && <p className="text-[10px] text-muted-foreground">{help}</p>}
    </div>
  )
}

function badgeVariantFor(type: ADR0073Step['type']): 'active' | 'in_review' | 'default' {
  if (type === 'approval' || type === 'signature') return 'active'
  if (type === 'conditional' || type === 'parallel') return 'in_review'
  return 'default'
}
