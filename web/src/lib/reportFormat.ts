// dimensionLabel renders one grouping value for a report chart axis /
// legend / table. A null, undefined, or empty dimension value (e.g. a
// document with no document_class) would otherwise map to '' and paint a
// bar with a blank x-axis label — visually indistinguishable from
// "missing data". Give the empty bucket an explicit, visible name so the
// chart is honest about what it's counting.
export function dimensionLabel(v: unknown): string {
  const s = v == null ? '' : String(v).trim()
  return s === '' ? '(uncategorized)' : s
}

// formatCell renders one result-table cell. Dimension cells reuse
// dimensionLabel so an empty bucket is visibly "(uncategorized)" in the
// table exactly as in the chart; numeric measure cells get thousands
// separators (raw byte/count values like 1421234 are hard to scan).
// en-US is pinned so the output is stable regardless of host locale.
export function formatCell(v: unknown, isDimension: boolean): string {
  if (isDimension) return dimensionLabel(v)
  if (typeof v === 'number' && Number.isFinite(v)) return v.toLocaleString('en-US')
  return v == null ? '' : String(v)
}
