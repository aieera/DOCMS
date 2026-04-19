// Wave 7 Prompt 7.5 — graph builder unit tests.
//
// We can't render ReactFlow under jsdom (it measures DOM rects),
// so these tests cover the pure layout function buildGraph.

import { describe, it, expect } from 'vitest'
import { buildGraph } from './WorkflowGraph'
import type { WorkflowStep } from '@/api/workflows'

describe('buildGraph', () => {
  it('returns only start + end for an empty workflow', () => {
    const { nodes, edges } = buildGraph([])
    expect(nodes.map((n) => n.id)).toEqual(['start', 'end'])
    expect(edges).toHaveLength(1)
    expect(edges[0]).toMatchObject({ source: 'start', target: 'end' })
  })

  it('lays out a linear chain with forward edges', () => {
    const steps: WorkflowStep[] = [
      { name: 'Review', type: 'review', assignee_id: 'alice' },
      { name: 'Approve', type: 'approval', assignee_id: 'bob' },
    ]
    const { nodes, edges } = buildGraph(steps)
    expect(nodes.map((n) => n.id)).toEqual(['start', 's-0', 's-1', 'end'])
    // start → s-0 → s-1 → end
    expect(edges).toHaveLength(3)
    expect(edges[0]).toMatchObject({ source: 'start', target: 's-0' })
    expect(edges[1]).toMatchObject({ source: 's-0', target: 's-1' })
    expect(edges[2]).toMatchObject({ source: 's-1', target: 'end' })
  })

  it('forks a condition into on_true / on_false subgraphs and rejoins', () => {
    const steps: WorkflowStep[] = [
      {
        name: 'Is invoice > $10k?',
        type: 'condition',
        condition: 'amount > 10000',
        on_true: [{ name: 'CFO approval', type: 'approval', assignee_id: 'cfo' }],
        on_false: [{ name: 'Manager approval', type: 'approval', assignee_id: 'mgr' }],
      },
      { name: 'Notify', type: 'notification' },
    ]
    const { nodes, edges } = buildGraph(steps)
    const ids = nodes.map((n) => n.id)
    expect(ids).toContain('s-0')
    expect(ids).toContain('s-0-T-0')
    expect(ids).toContain('s-0-F-0')
    expect(ids).toContain('s-0-J') // join marker inserted because a follow-up step exists
    expect(ids).toContain('s-1')

    const trueEdge = edges.find((e) => e.source === 's-0' && e.target === 's-0-T-0')
    expect(trueEdge?.label).toBe('true')
    const falseEdge = edges.find((e) => e.source === 's-0' && e.target === 's-0-F-0')
    expect(falseEdge?.label).toBe('false')

    // Both branch tails merge into the join node.
    expect(edges.some((e) => e.source === 's-0-T-0' && e.target === 's-0-J')).toBe(true)
    expect(edges.some((e) => e.source === 's-0-F-0' && e.target === 's-0-J')).toBe(true)
    // Join flows into the follow-up.
    expect(edges.some((e) => e.source === 's-0-J' && e.target === 's-1')).toBe(true)
  })

  it('skips the join marker when a condition is the final step', () => {
    const steps: WorkflowStep[] = [
      {
        name: 'Approved?',
        type: 'condition',
        condition: 'approved',
        on_true: [{ name: 'Archive', type: 'notification' }],
        on_false: [{ name: 'Escalate', type: 'notification' }],
      },
    ]
    const { nodes } = buildGraph(steps)
    expect(nodes.map((n) => n.id)).not.toContain('s-0-J')
  })
})
