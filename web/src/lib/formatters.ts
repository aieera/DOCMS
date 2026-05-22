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
export function formatFileSize(bytes: number | null | undefined): string {
  if (bytes == null || !Number.isFinite(bytes) || bytes < 0) return '—'
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB']
  const raw = Math.floor(Math.log(bytes) / Math.log(1024))
  const i = Math.min(raw, units.length - 1) // clamp for ZB+ inputs
  return `${(bytes / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0)} ${units[i]}`
}

export function formatDate(date: string | Date): string {
  return dayjs(date).format('MMM D, YYYY')
}

export function formatDateTime(date: string | Date): string {
  return dayjs(date).format('MMM D, YYYY h:mm A')
}

export function formatRelativeTime(date: string | Date): string {
  return dayjs(date).fromNow()
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
