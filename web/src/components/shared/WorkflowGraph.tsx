// Wave 7 Prompt 7.5 — read-only ReactFlow designer.
//
// Takes a WorkflowDefinition.steps tree (which may contain nested
// on_true / on_false branches for `condition` steps) and renders it as
// a directed graph. The layout is a deterministic top-down column
// walk: linear steps stack vertically, conditions fork left/right
// then rejoin at the next sibling. No dagre — the layouts we produce
// here are small (~20 nodes) and hand-tuned coordinates keep the
// bundle small.
//
// Read-only: handles are decorative, no drag/drop, no editing. The
// drag-to-create designer is explicitly out of scope (final.md § 14.1).

import { useMemo } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  MarkerType,
  type Edge,
  type Node,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import type { WorkflowStep } from '@/api/workflows'

const NODE_W = 200
const NODE_H = 60
const X_GAP = 60
const Y_GAP = 40
const ROW = NODE_H + Y_GAP

interface LayoutResult {
  nodes: Node[]
  edges: Edge[]
  height: number
}

function stepStyle(type: string): React.CSSProperties {
  const base: React.CSSProperties = {
    width: NODE_W,
    padding: '8px 12px',
    borderRadius: 8,
    border: '1px solid var(--color-border, #e2e8f0)',
    background: 'var(--color-bg-secondary, #fff)',
    fontSize: 13,
  }
  if (type === 'condition') {
    return { ...base, borderColor: '#f59e0b', background: '#fef3c7', color: '#78350f' }
  }
  if (type === 'approval') {
    return { ...base, borderColor: '#3b82f6', background: '#dbeafe', color: '#1e3a8a' }
  }
  if (type === 'signature') {
    return { ...base, borderColor: '#8b5cf6', background: '#ede9fe', color: '#5b21b6' }
  }
  return base
}

function nodeLabel(step: WorkflowStep): string {
  if (step.type === 'condition') {
    return `${step.name}\n${step.condition ?? ''}`
  }
  const who = step.assignee_id || step.assignee_group || (step.approvers ? `${step.approvers.length} approvers` : '')
  return `${step.name}${who ? ` · ${who}` : ''}`
}

// layoutSteps lays out `steps` vertically starting at (x, y). It
// returns all nodes/edges plus the bottom-of-block y value and the
// last node's id (so the caller can attach follow-on edges).
function layoutSteps(
  steps: WorkflowStep[],
  xCenter: number,
  yStart: number,
  idPrefix: string,
  parentId: string | null,
): { nodes: Node[]; edges: Edge[]; yEnd: number; lastId: string | null } {
  const nodes: Node[] = []
  const edges: Edge[] = []
  let y = yStart
  let prevId = parentId

  steps.forEach((step, idx) => {
    const id = `${idPrefix}-${idx}`
    nodes.push({
      id,
      position: { x: xCenter - NODE_W / 2, y },
      data: { label: nodeLabel(step) },
      style: stepStyle(step.type),
      draggable: false,
      selectable: false,
    })
    if (prevId) {
      edges.push({
        id: `e-${prevId}-${id}`,
        source: prevId,
        target: id,
        markerEnd: { type: MarkerType.ArrowClosed },
      })
    }
    y += ROW

    if (step.type === 'condition') {
      const trueSide = layoutSteps(step.on_true ?? [], xCenter - (NODE_W + X_GAP), y, `${id}-T`, id)
      const falseSide = layoutSteps(step.on_false ?? [], xCenter + (NODE_W + X_GAP), y, `${id}-F`, id)
      // Label the first edge of each branch.
      const firstTrue = trueSide.edges[0]
      if (firstTrue) firstTrue.label = 'true'
      const firstFalse = falseSide.edges[0]
      if (firstFalse) firstFalse.label = 'false'
      nodes.push(...trueSide.nodes, ...falseSide.nodes)
      edges.push(...trueSide.edges, ...falseSide.edges)
      y = Math.max(trueSide.yEnd, falseSide.yEnd)
      // After the branches we insert a dummy join id so the next step
      // (if any) receives both branches' tails as sources.
      const nextExists = idx < steps.length - 1
      if (nextExists) {
        const joinId = `${id}-J`
        nodes.push({
          id: joinId,
          position: { x: xCenter - 6, y },
          data: { label: '' },
          style: {
            width: 12,
            height: 12,
            borderRadius: 6,
            background: '#94a3b8',
            border: 'none',
          },
          draggable: false,
          selectable: false,
        })
        if (trueSide.lastId) edges.push({ id: `e-${trueSide.lastId}-${joinId}`, source: trueSide.lastId, target: joinId })
        else edges.push({ id: `e-${id}-${joinId}-T`, source: id, target: joinId, label: 'true' })
        if (falseSide.lastId) edges.push({ id: `e-${falseSide.lastId}-${joinId}`, source: falseSide.lastId, target: joinId })
        else edges.push({ id: `e-${id}-${joinId}-F`, source: id, target: joinId, label: 'false' })
        prevId = joinId
        y += ROW
      } else {
        // Last step in the list; the tails of both branches are the
        // block's exit points — we return the "true" tail by
        // convention since a caller's follow-up edge needs a single
        // source. Condition-at-tail with follow-up is rare.
        prevId = trueSide.lastId ?? falseSide.lastId ?? id
      }
    } else {
      prevId = id
    }
  })

  return { nodes, edges, yEnd: y, lastId: prevId }
}

// buildGraph produces the full node/edge set including start + end
// bookend nodes.
export function buildGraph(steps: WorkflowStep[]): LayoutResult {
  const xCenter = 300
  let y = 0
  const startId = 'start'
  const nodes: Node[] = [
    {
      id: startId,
      position: { x: xCenter - 40, y },
      data: { label: 'Start' },
      style: { width: 80, padding: 6, borderRadius: 999, background: '#10b981', color: '#fff', border: 'none' },
      draggable: false,
      selectable: false,
    },
  ]
  const edges: Edge[] = []
  y += ROW
  const body = layoutSteps(steps, xCenter, y, 's', startId)
  nodes.push(...body.nodes)
  edges.push(...body.edges)
  const endY = body.yEnd
  const endId = 'end'
  nodes.push({
    id: endId,
    position: { x: xCenter - 40, y: endY },
    data: { label: 'End' },
    style: { width: 80, padding: 6, borderRadius: 999, background: '#ef4444', color: '#fff', border: 'none' },
    draggable: false,
    selectable: false,
  })
  if (body.lastId) {
    edges.push({
      id: `e-${body.lastId}-end`,
      source: body.lastId,
      target: endId,
      markerEnd: { type: MarkerType.ArrowClosed },
    })
  }
  return { nodes, edges, height: endY + NODE_H }
}

export interface WorkflowGraphProps {
  steps: WorkflowStep[]
  height?: number
}

export function WorkflowGraph({ steps, height = 480 }: WorkflowGraphProps) {
  const { nodes, edges } = useMemo(() => buildGraph(steps), [steps])

  if (nodes.length <= 2) {
    return (
      <div
        className="flex items-center justify-center rounded-lg border border-dashed border-[var(--color-border)] text-sm text-[var(--color-text-secondary)]"
        style={{ height }}
      >
        This workflow has no steps defined.
      </div>
    )
  }

  return (
    <div style={{ height, width: '100%' }} className="rounded-lg border border-[var(--color-border)]">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        fitView
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        proOptions={{ hideAttribution: true }}
      >
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  )
}
