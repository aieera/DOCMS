// ADR 0064 — workflow designer. Drag-drop steps onto a canvas;
// each node maps 1:1 to an entry in the workflow JSON schema. Live
// validation surfaces errors per node (missing assignee, bad Rego,
// SLA out of range) before save.
//
// Limitations of the v1 designer (call them out so users know):
//   - Linear flow only — drag a node onto the canvas and it gets
//     appended to the steps array. Re-ordering is via the panel
//     on the right (move up / down). True branched layout (the
//     conditional pattern) is rendered as a"→ on_true / on_false
//     subgraph" line beneath the node rather than as separate
//     edges. Future work: full graph editor.
//   - Per-node validation is presentational; the canonical check
//     happens server-side (the schema in
//     docs/api/workflow-definition-schema.json).
import { useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { ReactFlow, ReactFlowProvider, Background, Controls, MiniMap, type Node, type Edge } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { Plus, Trash2, ArrowUp, ArrowDown, Save, AlertTriangle, CheckCircle2 } from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Badge } from '@/components/ui/shadcn/badge'
import type { ADR0073Step } from '@/api/workflows'

// errorComponent renders inside the AppLayout when the route function
// throws — without it, an exception in this designer (e.g. ReactFlow
// css-not-loaded edge case during HMR) renders nothing and looks like
// a blank page. pendingComponent shows the same layout chrome while
// the route is suspending so the topbar+sidebar don't pop in.
export const Route = createFileRoute('/_authenticated/workflows/designer')({
  component: WorkflowDesigner,
  errorComponent: WorkflowDesignerError,
  pendingComponent: WorkflowDesignerPending,
})

function WorkflowDesignerError({ error, reset }: { error: Error; reset: () => void }) {
  return (
    <div className="space-y-4">
      <PageHeader title="Workflow designer" description="Drag step types onto the canvas." />
      <div
        className="flex flex-col items-center justify-center rounded-lg border border-destructive/40 bg-destructive/5 p-12 text-center"
        data-testid="designer-error"
      >
        <AlertTriangle className="h-10 w-10 text-destructive" />
        <h3 className="mt-4 text-lg font-medium">Designer failed to load</h3>
        <p className="mt-1 max-w-md text-sm text-muted-foreground">
          {error?.message || 'Unknown render error. Please reload the page.'}
        </p>
        <Button className="mt-4" onClick={reset} data-testid="designer-reset">Try again</Button>
      </div>
    </div>
  )
}

function WorkflowDesignerPending() {
  return (
    <div className="space-y-4">
      <PageHeader title="Workflow designer" description="Drag step types onto the canvas." />
      <div className="h-[60vh] animate-pulse rounded-lg border border-border bg-muted/40" />
    </div>
  )
}

const STEP_PRESETS: { type: ADR0073Step['type']; label: string; defaults: Partial<ADR0073Step> }[] = [
  { type: 'approval',     label: 'Approval',      defaults: { sla_hours: 48, on_expire: 'escalate', allow_delegate: true } },
  { type: 'parallel',     label: 'Parallel',      defaults: { mode: 'require_all', sla_hours: 48, on_expire: 'auto_reject' } },
  { type: 'conditional',  label: 'Conditional',   defaults: { condition_rego: 'true', on_true: [], on_false: [] } },
  { type: 'notification', label: 'Notification',  defaults: {} },
  { type: 'signature',    label: 'Signature',     defaults: { sla_hours: 72, on_expire: 'auto_reject' } },
]

interface ValidationIssue { stepId: string; message: string }

function WorkflowDesigner() {
  const [name, setName] = useState('Untitled workflow')
  const [steps, setSteps] = useState<ADR0073Step[]>([])
  const [selectedID, setSelectedID] = useState<string | null>(null)

  const addStep = (preset: typeof STEP_PRESETS[number]) => {
    const id = `step-${Date.now().toString(36)}`
    const next: ADR0073Step = {
      id, type: preset.type, name: preset.label, ...preset.defaults,
    }
    setSteps((s) => [...s, next])
    setSelectedID(id)
  }
  const updateStep = (id: string, patch: Partial<ADR0073Step>) =>
    setSteps((s) => s.map((st) => (st.id === id ? { ...st, ...patch } : st)))
  const removeStep = (id: string) => setSteps((s) => s.filter((st) => st.id !== id))
  const moveStep = (id: string, dir: -1 | 1) =>
    setSteps((s) => {
      const i = s.findIndex((st) => st.id === id)
      if (i < 0 || (dir === -1 && i === 0) || (dir === 1 && i === s.length - 1)) return s
      const out = [...s]
      ;[out[i], out[i + dir]] = [out[i + dir], out[i]]
      return out
    })

  const issues = useMemo<ValidationIssue[]>(() => validate(steps), [steps])
  const selected = steps.find((s) => s.id === selectedID) ?? null

  // Translate the steps array into a ReactFlow graph. Linear chain
  // for v1; conditional steps render with a small fork annotation.
  const { nodes, edges } = useMemo(() => buildGraph(steps, issues), [steps, issues])

  return (
    // min-h instead of fixed h: the AppLayout already provides
    // viewport-aware sizing; a hard h-[calc(100vh-4rem)] fights with
    // it when the topbar height changes (mobile, dense mode) and can
    // collapse the canvas to 0px on certain viewports.
    <div className="flex min-h-[calc(100vh-8rem)] flex-col gap-3" data-testid="workflow-designer">
      <PageHeader
        title="Workflow designer"
        description="Drag step types onto the canvas. Configure routing in the panel on the right."
      />

      <div className="grid flex-1 grid-cols-1 gap-3 overflow-hidden lg:grid-cols-[16rem_1fr_22rem]">
        {/* ---- Left: palette + meta ---- */}
        <aside className="flex flex-col gap-3 overflow-y-auto rounded-lg border border-border bg-card p-3">
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} />
          <h3 className="mt-2 text-xs font-semibold uppercase text-muted-foreground">Add step</h3>
          {STEP_PRESETS.map((p) => (
            <Button key={p.type} variant="outline" size="sm" onClick={() => addStep(p)} className="justify-start">
              <Plus className="h-4 w-4" /> {p.label}
            </Button>
          ))}
          <div className="mt-3 text-xs text-muted-foreground">
            <ValidationSummary issues={issues} />
          </div>
          <Button
            data-testid="save-workflow"
            disabled={issues.length > 0 || steps.length === 0}
            className="mt-auto"
          >
            <Save className="h-4 w-4" /> Save workflow
          </Button>
        </aside>

        {/* ---- Center: canvas ---- */}
        {/* Explicit min-height — ReactFlow needs a sized parent or
            it computes 0×0 and renders nothing. The wrapping
            ReactFlowProvider lets MiniMap/Controls share state if a
            future iteration adds multiple instances. */}
        <div className="min-h-[480px] overflow-hidden rounded-lg border border-border bg-card">
          <ReactFlowProvider>
            <ReactFlow
              nodes={nodes}
              edges={edges}
              onNodeClick={(_, n) => setSelectedID(String(n.id))}
              fitView
              proOptions={{ hideAttribution: true }}
            >
              <MiniMap />
              <Controls />
              <Background />
            </ReactFlow>
          </ReactFlowProvider>
        </div>

        {/* ---- Right: step config ---- */}
        <aside className="flex flex-col gap-3 overflow-y-auto rounded-lg border border-border bg-card p-3">
          {!selected && <p className="text-sm text-muted-foreground">Select a step to configure.</p>}
          {selected && (
            <StepConfig
              step={selected}
              onChange={(p) => updateStep(selected.id, p)}
              onRemove={() => { removeStep(selected.id); setSelectedID(null) }}
              onMoveUp={() => moveStep(selected.id, -1)}
              onMoveDown={() => moveStep(selected.id, 1)}
              issues={issues.filter((i) => i.stepId === selected.id)}
            />
          )}
        </aside>
      </div>
    </div>
  )
}

// ---- step config panel ---------------------------------------------------

function StepConfig({ step, onChange, onRemove, onMoveUp, onMoveDown, issues }: {
  step: ADR0073Step
  onChange: (p: Partial<ADR0073Step>) => void
  onRemove: () => void
  onMoveUp: () => void
  onMoveDown: () => void
  issues: ValidationIssue[]
}) {
  return (
    <div className="space-y-3 text-sm">
      <div className="flex items-center justify-between">
        <Badge variant={step.type === 'approval' ? 'active' : step.type === 'conditional' ? 'in_review' : 'default'}>
          {step.type}
        </Badge>
        <div className="flex gap-1">
          <Button variant="ghost" size="sm" onClick={onMoveUp}><ArrowUp className="h-3.5 w-3.5" /></Button>
          <Button variant="ghost" size="sm" onClick={onMoveDown}><ArrowDown className="h-3.5 w-3.5" /></Button>
          <Button variant="ghost" size="sm" onClick={onRemove}><Trash2 className="h-3.5 w-3.5" /></Button>
        </div>
      </div>
      <Input label="Display name" value={step.name ?? ''} onChange={(e) => onChange({ name: e.target.value })} />
      <Input label="Step id (immutable)" value={step.id} readOnly />

      {step.type === 'approval' && (
        <>
          <Select
            label="Assignee type"
            value={step.assignee?.type ?? 'user'}
            onValueChange={(v) => onChange({ assignee: { type: v as any, value: step.assignee?.value ?? '' } })}
            options={[
              { value: 'user',    label: 'User' },
              { value: 'group',   label: 'Group' },
              { value: 'dynamic', label: 'Dynamic (e.g. document.created_by)' },
            ]}
          />
          <Input
            label="Assignee value"
            value={step.assignee?.value ?? ''}
            onChange={(e) => onChange({ assignee: { type: step.assignee?.type ?? 'user', value: e.target.value } })}
          />
        </>
      )}

      {step.type === 'parallel' && (
        <>
          <Select
            label="Mode"
            value={step.mode ?? 'require_all'}
            onValueChange={(v) => onChange({ mode: v as any })}
            options={[
              { value: 'require_all', label: 'All must approve' },
              { value: 'require_any', label: 'First to approve wins' },
            ]}
          />
          <Input
            label="Approvers (comma-separated UUIDs)"
            value={(step.approvers ?? []).join(',')}
            onChange={(e) => onChange({ approvers: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) })}
          />
        </>
      )}

      {step.type === 'conditional' && (
        <>
          <Input
            label="Rego expression"
            value={step.condition_rego ?? ''}
            onChange={(e) => onChange({ condition_rego: e.target.value })}
            placeholder="input.document.custom_metadata.amount > 100000"
          />
          <Input
            label="on_true step ids (comma-separated)"
            value={(step.on_true ?? []).join(',')}
            onChange={(e) => onChange({ on_true: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) })}
          />
          <Input
            label="on_false step ids (comma-separated)"
            value={(step.on_false ?? []).join(',')}
            onChange={(e) => onChange({ on_false: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) })}
          />
        </>
      )}

      {(step.type === 'approval' || step.type === 'parallel' || step.type === 'signature') && (
        <>
          <Input
            label="SLA (hours)"
            type="number"
            value={step.sla_hours ?? 0}
            onChange={(e) => onChange({ sla_hours: Number(e.target.value) })}
          />
          <Select
            label="On SLA expiry"
            value={step.on_expire ?? 'escalate'}
            onValueChange={(v) => onChange({ on_expire: v as any })}
            options={[
              { value: 'escalate',     label: 'Escalate' },
              { value: 'auto_approve', label: 'Auto-approve' },
              { value: 'auto_reject',  label: 'Auto-reject' },
            ]}
          />
        </>
      )}

      {step.on_expire === 'escalate' && (
        <Select
          label="Escalation strategy"
          value={step.escalation?.strategy ?? 'manager'}
          onValueChange={(v) => onChange({ escalation: { ...(step.escalation ?? {}), strategy: v as any } })}
          options={[
            { value: 'manager', label: 'Up the manager chain' },
            { value: 'fixed',   label: 'Fixed user' },
            { value: 'chain',   label: 'Explicit chain' },
          ]}
        />
      )}

      {issues.length > 0 && (
        <ul className="space-y-1 rounded border border-warning/40 bg-warning/10 p-2 text-xs text-warning">
          {issues.map((i, k) => (
            <li key={k} className="flex items-start gap-1"><AlertTriangle className="mt-0.5 h-3 w-3" /> {i.message}</li>
          ))}
        </ul>
      )}
    </div>
  )
}

// ---- validation ----------------------------------------------------------

function validate(steps: ADR0073Step[]): ValidationIssue[] {
  const issues: ValidationIssue[] = []
  const ids = new Set<string>()
  for (const s of steps) {
    if (ids.has(s.id)) issues.push({ stepId: s.id, message: `duplicate id"${s.id}"` })
    ids.add(s.id)
    if (s.type === 'approval' && !s.assignee?.value) {
      issues.push({ stepId: s.id, message: 'approval needs an assignee' })
    }
    if (s.type === 'parallel') {
      if (!s.approvers || s.approvers.length < 2) {
        issues.push({ stepId: s.id, message: 'parallel needs ≥2 approvers' })
      }
      if (!s.mode) issues.push({ stepId: s.id, message: 'parallel needs a mode' })
    }
    if (s.type === 'conditional' && !s.condition_rego?.trim()) {
      issues.push({ stepId: s.id, message: 'conditional needs a Rego expression' })
    }
    if (s.sla_hours != null && (s.sla_hours < 1 || s.sla_hours > 720)) {
      issues.push({ stepId: s.id, message: 'sla_hours must be 1..720' })
    }
  }
  // Cross-references: on_true/on_false ids must exist.
  for (const s of steps) {
    if (s.type !== 'conditional') continue
    for (const ref of [...(s.on_true ?? []), ...(s.on_false ?? [])]) {
      if (!ids.has(ref)) issues.push({ stepId: s.id, message: `unknown step id"${ref}" in branch` })
    }
  }
  return issues
}

function ValidationSummary({ issues }: { issues: ValidationIssue[] }) {
  if (issues.length === 0) {
    return (
      <span className="inline-flex items-center gap-1 text-xs text-success dark:text-emerald-300">
        <CheckCircle2 className="h-3 w-3" /> Definition valid
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1 text-xs text-warning">
      <AlertTriangle className="h-3 w-3" /> {issues.length} issue{issues.length > 1 ? 's' : ''}
    </span>
  )
}

// ---- ReactFlow plumbing --------------------------------------------------

function buildGraph(steps: ADR0073Step[], issues: ValidationIssue[]): { nodes: Node[]; edges: Edge[] } {
  const issuesByStep = new Map<string, number>()
  for (const i of issues) issuesByStep.set(i.stepId, (issuesByStep.get(i.stepId) ?? 0) + 1)

  const nodes: Node[] = steps.map((s, i) => ({
    id: s.id,
    position: { x: 80, y: 80 + i * 120 },
    data: { label: nodeLabel(s, issuesByStep.get(s.id) ?? 0) },
    style: nodeStyle(s, issuesByStep.has(s.id)),
  }))
  const edges: Edge[] = []
  for (let i = 0; i < steps.length - 1; i++) {
    edges.push({ id: `e-${steps[i].id}-${steps[i + 1].id}`, source: steps[i].id, target: steps[i + 1].id })
  }
  return { nodes, edges }
}

function nodeLabel(s: ADR0073Step, issueCount: number): string {
  const tail = issueCount > 0 ? `  ⚠ ${issueCount}` : ''
  return `${s.name ?? s.id}\n[${s.type}]${tail}`
}

function nodeStyle(s: ADR0073Step, hasIssue: boolean): React.CSSProperties {
  return {
    border: hasIssue ? '2px solid #f59e0b' : '1px solid #94a3b8',
    background: s.type === 'conditional' ? '#fef3c7' : '#fff',
    padding: 8,
    borderRadius: 8,
    fontSize: 12,
    whiteSpace: 'pre',
    minWidth: 180,
  }
}
