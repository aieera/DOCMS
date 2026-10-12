import { describe, it, expect } from 'vitest'
import { toCSV } from '@/api/analytics'

// W-9: report cells come from tenant data (document titles, tag names,
// user names). A cell starting with = + - @ (or tab / CR) is executed as
// a formula when the CSV is opened in Excel/Sheets — classic CSV
// injection. Such cells are prefixed with a single quote.
describe('toCSV formula guard (W-9)', () => {
  const run = (cells: unknown[]) =>
    toCSV({ columns: ['c'], rows: cells.map((c) => [c]) } as never).split('\r\n').slice(1)

  it('neutralises formula-leading cells', () => {
    expect(run(['=HYPERLINK("http://evil","x")'])).toEqual([`"'=HYPERLINK(""http://evil"",""x"")"`])
    expect(run(['+1+1', '-2+3', '@SUM(A1)'])).toEqual(["'+1+1", "'-2+3", "'@SUM(A1)"])
    expect(run(['\tcmd', '\rcmd'])).toEqual(["'\tcmd", `"'\rcmd"`])
  })

  it('guards column headers too', () => {
    expect(toCSV({ columns: ['=1+1'], rows: [] } as never)).toBe("'=1+1")
  })

  it('leaves numbers and ordinary text alone', () => {
    expect(run([42, -7, 3.5, 'Contracts', 'a,b', null])).toEqual(['42', '-7', '3.5', 'Contracts', '"a,b"', ''])
  })
})
