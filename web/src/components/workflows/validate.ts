import type { ADR0073Step } from '@/api/workflows'

// ValidationIssue ties a single error to the step that triggered it.
// stepId is the ADR0073Step.id field; the editor highlights the
// matching chip in the chain list and the field in the config panel.
export interface ValidationIssue {
  stepId: string
  message: string
}

// validateSteps returns every issue across the whole chain. Pure
// function — no React, no I/O — so the same logic powers the
// template builder's live banner, the "Save" button's disabled
// state, and the admin-side health column for a saved template.
//
// Rules:
//   - step ids must be unique inside a chain
//   - approval / signature need an assignee
//   - parallel needs >= 2 branches and a mode
//   - conditional needs a Rego expression
//   - sla_hours, when set, must be 1..720
//   - on_true / on_false branches must reference step ids that
//     actually exist in the chain
export function validateSteps(steps: ADR0073Step[]): ValidationIssue[] {
  const issues: ValidationIssue[] = []
  const ids = new Set<string>()
  for (const s of steps) {
    if (ids.has(s.id)) {
      issues.push({ stepId: s.id, message: `Duplicate step id: ${s.id}` })
    }
    ids.add(s.id)
    if ((s.type === 'approval' || s.type === 'signature') && !s.assignee?.value) {
      issues.push({
        stepId: s.id,
        message: `${s.type === 'signature' ? 'Signature' : 'Approval'} step needs an assignee`,
      })
    }
    if (s.type === 'parallel') {
      if (!s.approvers || s.approvers.length < 2) {
        issues.push({ stepId: s.id, message: 'Parallel step needs at least 2 branches' })
      }
      if (!s.mode) {
        issues.push({ stepId: s.id, message: 'Parallel step needs a join mode' })
      }
    }
    if (s.type === 'conditional' && !s.condition_rego?.trim()) {
      issues.push({ stepId: s.id, message: 'Conditional step needs a Rego expression' })
    }
    if (s.sla_hours != null && (s.sla_hours < 1 || s.sla_hours > 720)) {
      issues.push({ stepId: s.id, message: 'SLA must be between 1 and 720 hours' })
    }
  }
  for (const s of steps) {
    if (s.type !== 'conditional') continue
    for (const ref of [...(s.on_true ?? []), ...(s.on_false ?? [])]) {
      if (!ids.has(ref)) {
        issues.push({ stepId: s.id, message: `Branch references missing step id: ${ref}` })
      }
    }
  }
  return issues
}

// issuesByStep buckets the flat issue list by stepId so a config
// panel rendering one step can pull out its own issues without
// re-walking the array.
export function issuesByStep(issues: ValidationIssue[]): Record<string, ValidationIssue[]> {
  const out: Record<string, ValidationIssue[]> = {}
  for (const i of issues) {
    if (!out[i.stepId]) out[i.stepId] = []
    out[i.stepId].push(i)
  }
  return out
}
