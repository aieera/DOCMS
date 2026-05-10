import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
dayjs.extend(relativeTime)

export function formatFileSize(bytes: number | null | undefined): string {
  if (bytes == null || Number.isNaN(bytes)) return '—'
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
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
