// QA SD-11: activity feeds surfaced raw event-bus topics
// ("dms.notify.document.uploaded.v1") where a summary was missing or the
// producer stored the topic as the summary. The topic taxonomy
// (dms.{domain}.{action}.vN) and its schema version are implementation
// detail — anything topic-shaped becomes product vocabulary here, and
// real sentences pass through untouched.

import i18n from '@/i18n'

// English fallbacks AND the canonical topic list. The catalogue is the
// display source (event.*); these values are what an untranslated locale
// falls back to, and what the unit tests assert.
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

// De-slugged acronyms come out lowercased ("Ocr quality completed" —
// SD-11 retest); restore the casing readers expect.
const ACRONYMS: Record<string, string> = {
  ocr: 'OCR', pii: 'PII', phi: 'PHI', ai: 'AI', llm: 'LLM', erp: 'ERP',
  irm: 'IRM', sso: 'SSO', scim: 'SCIM', ldap: 'LDAP', mfa: 'MFA',
  api: 'API', ner: 'NER', siem: 'SIEM', dlq: 'DLQ',
}

export function humanizeEventSummary(summary: string): string {
  const m = TOPIC_RE.exec(summary.trim())
  if (!m) return summary
  const key = m[1]
  // Unknown topic: strip the taxonomy scaffolding and sentence-case what
  // remains ("workflow.step_completed" → "Workflow step completed"). This
  // is also the defaultValue below, so a topic the catalogue has not
  // caught up with still reads as words rather than a raw subject.
  const words = key
    .split(/[._]/)
    .map((w) => ACRONYMS[w] ?? w)
    .join(' ')
  const fallback = TOPIC_LABELS[key] ?? words.charAt(0).toUpperCase() + words.slice(1)
  // i18next treats "." as a key separator, so the topic path becomes
  // underscores: document.created -> event.document_created.
  return i18n.t(`event.${key.replace(/\./g, '_')}`, { ns: 'common', defaultValue: fallback })
}
