import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
import i18n from '@/i18n'
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
// Byte-unit symbols come from the catalogue: Arabic uses بايت / ك.ب / م.ب
// rather than the Latin abbreviations. EN values match the old literals,
// so English output is unchanged.
const sizeUnit = (key: string) =>
  i18n.t(`unit.${key}`, { ns: 'common', defaultValue: key.toUpperCase() })

export function formatFileSize(bytes: number | string | null | undefined): string {
  const n = typeof bytes === 'string' ? (bytes.trim() === '' ? NaN : Number(bytes)) : bytes
  if (n == null || !Number.isFinite(n) || n < 0) return '—'
  if (n === 0) return `0\u00A0${sizeUnit('b')}`
  const units = ['b', 'kb', 'mb', 'gb', 'tb', 'pb', 'eb']
  const raw = Math.floor(Math.log(n) / Math.log(1024))
  const i = Math.min(raw, units.length - 1) // clamp for ZB+ inputs
  // LRI…PDI (U+2066/U+2069) bidi-isolate the "<number> <unit>" pair, and
  // NBSP glues it: under RTL the neutral pair re-ordered as "KB 7.9"
  // (QA SD-07). Isolation keeps internal LTR order in any context.
  return `\u2066${(n / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0)}\u00A0${sizeUnit(units[i])}\u2069`
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

// Dates follow the UI language. These are plain functions, not hooks —
// they are called from ~100 places including non-component code — so
// they read the active language off the i18next singleton rather than
// taking it as an argument. i18n does not import this module, so there
// is no cycle; before init (and in unit tests) `language` is undefined
// and everything falls back to English.
//
// `-u-nu-latn` pins the numbering system to Latin digits. Without it the
// `ar` locale renders ٢٠٢٦, and the requirement is Arabic month names
// with ordinary numerals.
function intlLocale(): string {
  const lng = (i18n?.language || 'en').split('-')[0]
  return `${lng}-u-nu-latn`
}

// Intl formatter construction is genuinely expensive and these run in
// table-row loops, so keep one per locale+shape.
const dateFmts = new Map<string, Intl.DateTimeFormat>()
function dateFmt(opts: Intl.DateTimeFormatOptions, tag: string): Intl.DateTimeFormat {
  const locale = intlLocale()
  const key = `${locale}|${tag}`
  let f = dateFmts.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat(locale, opts)
    dateFmts.set(key, f)
  }
  return f
}

export function formatDate(date: string | Date | null | undefined): string {
  if (!isUsefulDate(date)) return '—'
  return dateFmt({ day: 'numeric', month: 'short', year: 'numeric' }, 'date')
    .format(new Date(date as string | Date))
}

export function formatDateTime(date: string | Date | null | undefined): string {
  if (!isUsefulDate(date)) return '—'
  return dateFmt(
    { day: 'numeric', month: 'short', year: 'numeric', hour: 'numeric', minute: '2-digit' },
    'datetime',
  ).format(new Date(date as string | Date))
}

// Thresholds match dayjs's fromNow() closely enough that the English
// copy barely moves, but Intl supplies the Arabic wording, which dayjs
// cannot without shipping its own locale bundle (and that bundle would
// also force Arabic-Indic digits).
const REL_UNITS: [limitSeconds: number, perUnit: number, unit: Intl.RelativeTimeFormatUnit][] = [
  [45, 1, 'second'],
  [45 * 60, 60, 'minute'],
  [22 * 3600, 3600, 'hour'],
  [26 * 86400, 86400, 'day'],
  [11 * 2592000, 2592000, 'month'],
  [Number.POSITIVE_INFINITY, 31536000, 'year'],
]
const relFmts = new Map<string, Intl.RelativeTimeFormat>()

export function formatRelativeTime(date: string | Date | null | undefined): string {
  if (!isUsefulDate(date)) return '—'
  const locale = intlLocale()
  let f = relFmts.get(locale)
  if (!f) {
    f = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' })
    relFmts.set(locale, f)
  }
  const deltaSec = (new Date(date as string | Date).getTime() - Date.now()) / 1000
  const abs = Math.abs(deltaSec)
  const [, perUnit, unit] = REL_UNITS.find(([limit]) => abs < limit) ?? REL_UNITS[REL_UNITS.length - 1]
  return f.format(Math.round(deltaSec / perUnit), unit)
}

// Map proto-style lifecycle state enums (LIFECYCLE_STATE_DRAFT) to
// human-friendly labels. Falls back to a Title-Cased version of the
// raw value when an unknown state arrives so the UI never shows a
// SHOUTY_SNAKE string. Lower-case canonical values (draft, active,
// …) come from REST endpoints that pre-strip the prefix; both shapes
// land in the same spot.
// Lifecycle states arrive in two shapes — the proto enum
// (LIFECYCLE_STATE_DRAFT) and the REST lower-case form (draft) — so both
// normalise to one catalogue key. An unknown state still never shows a
// SHOUTY_SNAKE string: it falls back to a Title-Cased version of itself,
// which is also what an untranslated locale gets.
export function lifecycleStateLabel(state: string | null | undefined): string {
  if (!state) return '\u2014'
  const base = state.replace(/^LIFECYCLE_STATE_/i, '').replace(/_/g, ' ').trim().toLowerCase()
  const key = base.replace(/ /g, '_')
  const titled = base.charAt(0).toUpperCase() + base.slice(1)
  return i18n.t(`lifecycle.${key}`, { ns: 'common', defaultValue: titled })
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
// Irregular codes get an explicit catalogue entry; everything else is
// derived (translated domain noun + humanised action) so a newly-emitted
// event type still reads sensibly instead of leaking the raw code.
// Audit `resource_type` is an open set the backend keeps adding to
// (document, folder, siem_sink, privacy_dsr_request, …), so an unknown
// value de-slugs rather than falling through as a raw token.
export function resourceTypeLabel(type: string | null | undefined): string {
  if (!type) return ''
  const words = type.replace(/_/g, ' ')
  const titled = words.charAt(0).toUpperCase() + words.slice(1)
  return i18n.t(`resource.${type}`, { ns: 'common', defaultValue: titled })
}

export function notificationTypeLabel(type: string | null | undefined): string {
  const T = (k: string, dflt: string) => i18n.t(k, { ns: 'common', defaultValue: dflt })
  if (!type) return T('notif_type.fallback', 'Notification')
  const explicit = i18n.t(`notif_type.${type}`, { ns: 'common', defaultValue: '' })
  if (explicit) return explicit
  // Digest rows carry a `digest.{domain}` type; the domain is noise to
  // the reader — what matters is that several events were bundled.
  if (type.startsWith('digest.')) return T('notif_type.digest', 'Digest')
  const [domain, ...rest] = type.split('.')
  const noun = i18n.t(`notif_domain.${domain}`, { ns: 'common', defaultValue: '' })
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
    'text/plain': i18n.t('mime.text', { ns: 'common', defaultValue: 'Text' }),
    'video/mp4': i18n.t('mime.video', { ns: 'common', defaultValue: 'Video' }),
  }
  return map[mime] || mime.split('/').pop()?.toUpperCase()
    || i18n.t('mime.file', { ns: 'common', defaultValue: 'File' })
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
