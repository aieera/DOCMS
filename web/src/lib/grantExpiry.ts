// L-12: an <input type="date"> value means "through the end of that day"
// in the user's own timezone. Sending `${date}T00:00:00Z` expired the
// grant at the START of the day in UTC (the previous evening west of
// UTC). These helpers convert between the date input and the instant.

const DATE_RE = /^(\d{4})-(\d{2})-(\d{2})$/

/** 'YYYY-MM-DD' → ISO instant of 23:59:59.999 local time that day ('' if invalid). */
export function endOfLocalDayISO(date: string): string {
  const m = DATE_RE.exec(date)
  if (!m) return ''
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 23, 59, 59, 999)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString()
}

/** ISO instant → the local 'YYYY-MM-DD' for a date input ('' if empty/invalid). */
export function isoToLocalDateInput(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
