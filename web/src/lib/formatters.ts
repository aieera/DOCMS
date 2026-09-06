import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
dayjs.extend(relativeTime)

// Human-readable file size. Edge cases (C-2):
//   - null / undefined / NaN  → em-dash placeholder
//   - negative                 → em-dash (size can't be negative; the
//                                old code returned "NaN undefined")
//   - 0                        → "0 B"
//   - >= 1 PB                  → "PB" / "EB" instead of running off the
//                                end of the units array as "… undefined"
//   - non-finite (±Infinity)   → em-dash
// Accepts strings too: the API serialises int64 byte counts as JSON
// strings ("15907"), and callers kept forgetting the Number() wrapper —
// which silently rendered "—" for perfectly good sizes.
export function formatFileSize(bytes: number | string | null | undefined): string {
  const n = typeof bytes === 'string' ? (bytes.trim() === '' ? NaN : Number(bytes)) : bytes
  if (n == null || !Number.isFinite(n) || n < 0) return '—'
  if (n === 0) return '0\u00A0B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB']
  const raw = Math.floor(Math.log(n) / Math.log(1024))
  const i = Math.min(raw, units.length - 1) // clamp for ZB+ inputs
  // LRI…PDI (U+2066/U+2069) bidi-isolate the "<number> <unit>" pair, and
  // NBSP glues it: under RTL the neutral pair re-ordered as "KB 7.9"
  // (QA SD-07). Isolation keeps internal LTR order in any context.
  return `\u2066${(n / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0)}\u00A0${units[i]}\u2069`
}

// Reject obviously-bogus input (null/undefined/empty, invalid, or the
// Go zero-time "0001-01-01T00:00:00Z" that leaks through when a
// time.Time field is unpopulated). Search hits in particular have
// surfaced "2025 years ago" when the indexer skipped created_at.
function isUsefulDate(date: string | Date | null | undefined): boolean {
  if (date == null || date === '') return false
  const d = dayjs(date)
  return d.isValid() && d.year() > 1900
}

export function formatDate(date: string | Date | null | undefined): string {
  if (!isUsefulDate(date)) return '—'
  return dayjs(date as string | Date).format('MMM D, YYYY')
}

export function formatDateTime(date: string | Date | null | undefined): string {
  if (!isUsefulDate(date)) return '—'
  return dayjs(date as string | Date).format('MMM D, YYYY h:mm A')
}

export function formatRelativeTime(date: string | Date | null | undefined): string {
  if (!isUsefulDate(date)) return '—'
  return dayjs(date as string | Date).fromNow()
}

// Map proto-style lifecycle state enums (LIFECYCLE_STATE_DRAFT) to
// human-friendly labels. Falls back to a Title-Cased version of the
// raw value when an unknown state arrives so the UI never shows a
// SHOUTY_SNAKE string. Lower-case canonical values (draft, active,
// …) come from REST endpoints that pre-strip the prefix; both shapes
// land in the same spot.
const LIFECYCLE_LABELS: Record<string, string> = {
  LIFECYCLE_STATE_UNSPECIFIED: 'Unknown',
  LIFECYCLE_STATE_DRAFT: 'Draft',
  LIFECYCLE_STATE_IN_REVIEW: 'In review',
  LIFECYCLE_STATE_ACTIVE: 'Active',
  LIFECYCLE_STATE_PUBLISHED: 'Published',
  LIFECYCLE_STATE_SUPERSEDED: 'Superseded',
  LIFECYCLE_STATE_RETAINED: 'Retained',
  LIFECYCLE_STATE_ARCHIVED: 'Archived',
  LIFECYCLE_STATE_DISPOSED: 'Disposed',
  LIFECYCLE_STATE_LEGAL_HOLD: 'Legal hold',
  draft: 'Draft',
  in_review: 'In review',
  active: 'Active',
  published: 'Published',
  superseded: 'Superseded',
  retained: 'Retained',
  archived: 'Archived',
  disposed: 'Disposed',
  legal_hold: 'Legal hold',
}

export function lifecycleStateLabel(state: string | null | undefined): string {
  if (!state) return '—'
  if (LIFECYCLE_LABELS[state]) return LIFECYCLE_LABELS[state]
  // Strip a LIFECYCLE_STATE_ prefix if present, then Title Case.
  const base = state.replace(/^LIFECYCLE_STATE_/i, '').replace(/_/g, ' ').toLowerCase()
  return base.charAt(0).toUpperCase() + base.slice(1)
}

// formatUsd renders LLM/API spend. Cost figures are frequently sub-cent,
// so a fixed 2-decimal money format is useless — but a fixed 5-decimal
// one is worse ("$0.00230" reads like a rounding bug, and the trailing
// zero is precision the number doesn't carry). Scale the precision to
// the magnitude instead and trim trailing zeros.
export function formatUsd(value: number | null | undefined): string {
  const n = Number(value)
  if (!Number.isFinite(n) || n === 0) return '—'
  if (n >= 1) return `$${n.toFixed(2)}`
  if (n >= 0.01) return `$${n.toFixed(3)}`
  if (n >= 0.0001) return `$${n.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')}`
  return '< $0.0001'
}

// Notification `type` values are raw event codes from the
// dms.{domain}.{action}.v1 taxonomy (e.g. "document.uploaded"). They are
// an internal contract and must never reach a user-facing surface, so
// every notification UI runs the code through this labeller first.
//
// Irregular codes get an explicit entry; everything else is derived
// (domain noun + humanised action) so a newly-emitted event type still
// reads sensibly instead of leaking the raw code.
const NOTIFICATION_DOMAIN_LABELS: Record<string, string> = {
  document: 'Document',
  comment: 'Comment',
  workflow: 'Workflow',
  task: 'Task',
  signature: 'Signature',
  security: 'Security',
  auth: 'Sign-in',
  saved_search: 'Saved search',
  compliance: 'Compliance',
  legal_hold: 'Legal hold',
  mention: 'Mention',
  share: 'Share',
  retention: 'Retention',
}

const NOTIFICATION_TYPE_LABELS: Record<string, string> = {
  'document.version_uploaded': 'New version uploaded',
  'comment.mention': 'Mentioned in a comment',
  'task.mention': 'Mentioned in a task comment',
  'task.due_soon': 'Task due soon',
  'workflow.step_assigned': 'Workflow step assigned',
}

export function notificationTypeLabel(type: string | null | undefined): string {
  if (!type) return 'Notification'
  if (NOTIFICATION_TYPE_LABELS[type]) return NOTIFICATION_TYPE_LABELS[type]
  // Digest rows carry a `digest.{domain}` type; the domain is noise to
  // the reader — what matters is that several events were bundled.
  if (type.startsWith('digest.')) return 'Digest'
  const [domain, ...rest] = type.split('.')
  const noun = NOTIFICATION_DOMAIN_LABELS[domain]
  const action = rest.join(' ').replace(/[._]/g, ' ').trim()
  if (!noun) {
    const words = type.replace(/[._]/g, ' ').trim()
    return words.charAt(0).toUpperCase() + words.slice(1)
  }
  return action ? `${noun} ${action}` : noun
}

export function getMimeTypeLabel(mime: string): string {
  const map: Record<string, string> = {
    'application/pdf': 'PDF',
    'image/jpeg': 'JPEG',
    'image/png': 'PNG',
    'application/vnd.openxmlformats-officedocument.wordprocessingml.document': 'Word',
    'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': 'Excel',
    'application/vnd.openxmlformats-officedocument.presentationml.presentation': 'PowerPoint',
    'text/plain': 'Text',
    'video/mp4': 'Video',
  }
  return map[mime] || mime.split('/').pop()?.toUpperCase() || 'File'
}

// formatShortId — render a UUID as a compact, copy-friendly identifier.
// Stripe-style: a type prefix + the last 8 hex chars of the UUID (which
// for v7 UUIDs are random, giving good visual distinction across tenants).
// The full UUID stays around as the underlying identifier; this is for
// display only.
export function formatShortId(prefix: string, uuid: string | undefined | null): string {
  if (!uuid) return '—'
  const hex = uuid.replace(/-/g, '')
  if (hex.length < 8) return uuid
  return `${prefix}_${hex.slice(-8)}`
}
