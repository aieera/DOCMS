/**
 * The text alternative for a chart. A chart announced only as one
 * `role="img"` label gives a screen-reader user the shape but not the
 * numbers; this puts the numbers in the accessibility tree without
 * showing a second copy on screen.
 */
export function ChartDataTable({
  caption,
  columns,
  rows,
}: {
  caption: string
  columns: [string, string]
  rows: { key: string; label: string; value: string | number }[]
}) {
  if (!rows.length) return null
  return (
    <table className="sr-only">
      <caption>{caption}</caption>
      <thead>
        <tr>
          <th scope="col">{columns[0]}</th>
          <th scope="col">{columns[1]}</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.key}>
            <td>{r.label}</td>
            <td>{r.value}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
