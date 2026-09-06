// QA SD-11: activity feeds surfaced raw event-bus topics
// ("dms.notify.document.uploaded.v1") where a summary was missing or the
// producer stored the topic as the summary. The topic taxonomy
// (dms.{domain}.{action}.vN) and its schema version are implementation
// detail — anything topic-shaped becomes product vocabulary here, and
// real sentences pass through untouched.

const TOPIC_LABELS: Record<string, string> = {
  'document.created': 'Document created',
  'notify.document.uploaded': 'Content uploaded',
  'version.uploaded': 'New version uploaded',
  'document.updated': 'Details updated',
  'document.deleted': 'Moved to Trash',
  'document.restored': 'Restored from Trash',
  'folder.created': 'Folder created',
  'folder.deleted': 'Folder moved to Trash',
  'folder.restored': 'Folder restored from Trash',
  'version.ocr_completed': 'Text extraction finished',
  'signature.completed': 'Signature completed',
  'signature.requested': 'Signature requested',
  'permission.changed': 'Access changed',
}

const TOPIC_RE = /^dms\.([a-z0-9_]+(?:\.[a-z0-9_]+)*)\.v\d+$/

export function humanizeEventSummary(summary: string): string {
  const m = TOPIC_RE.exec(summary.trim())
  if (!m) return summary
  const key = m[1]
  const known = TOPIC_LABELS[key]
  if (known) return known
  // Unknown topic: strip the taxonomy scaffolding and sentence-case what
  // remains ("workflow.step_completed" → "Workflow step completed").
  const words = key.replace(/[._]/g, ' ')
  return words.charAt(0).toUpperCase() + words.slice(1)
}
