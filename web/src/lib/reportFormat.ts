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
