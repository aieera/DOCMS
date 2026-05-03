// HighlightedText renders a block of text with entity spans rendered
// inline as <mark> elements, color-coded per entity type via the same
// ENTITY_COLOR map the Entities panel uses. Pure rendering — no data
// fetching. Caller passes `text` (matching the offsets the entities
// were extracted from) and the entity list.
//
// Algorithm: sort entities by start_offset and walk the text once,
// emitting alternating plain segments and <mark> spans. Overlapping
// spans are dropped (the dedupe step in the NER pipeline already
// removes them; this is belt + suspenders so a buggy server can't
// produce garbled HTML).
import { Fragment } from 'react'
import type { Entity } from '@/api/ner'
import { ENTITY_COLOR } from './EntitiesPanel'

interface Props {
  text: string
  entities: Entity[]
  onClickEntity?: (e: Entity) => void
}

interface Segment {
  start: number
  end: number
  entity?: Entity
}

function buildSegments(text: string, entities: Entity[]): Segment[] {
  // Sort by start, then longer-span first so we drop nested duplicates.
  const sorted = [...entities].sort((a, b) => {
    if (a.start_offset !== b.start_offset) return a.start_offset - b.start_offset
    return (b.end_offset - b.start_offset) - (a.end_offset - a.start_offset)
  })

  const segments: Segment[] = []
  let cursor = 0
  for (const e of sorted) {
    if (e.start_offset >= text.length) break
    const s = Math.max(e.start_offset, cursor)
    const t = Math.min(e.end_offset, text.length)
    if (s >= t) continue        // overlap with previous emitted entity
    if (s > cursor) segments.push({ start: cursor, end: s })
    segments.push({ start: s, end: t, entity: e })
    cursor = t
  }
  if (cursor < text.length) segments.push({ start: cursor, end: text.length })
  return segments
}

export function HighlightedText({ text, entities, onClickEntity }: Props) {
  const segments = buildSegments(text, entities)
  return (
    <pre className="whitespace-pre-wrap break-words font-mono text-xs leading-relaxed">
      {segments.map((seg, i) => {
        const slice = text.slice(seg.start, seg.end)
        if (!seg.entity) return <Fragment key={i}>{slice}</Fragment>
        const color = ENTITY_COLOR[seg.entity.entity_type] ?? 'bg-zinc-100 text-zinc-900 border-zinc-200'
        return (
          <mark
            key={i}
            data-testid={`entity-mark-${seg.entity.entity_type}`}
            title={`${seg.entity.entity_type} · ${(seg.entity.confidence * 100).toFixed(0)}% · ${seg.entity.source}`}
            onClick={() => onClickEntity?.(seg.entity!)}
            className={`cursor-pointer rounded border px-0.5 ${color}`}
          >
            {slice}
          </mark>
        )
      })}
    </pre>
  )
}
