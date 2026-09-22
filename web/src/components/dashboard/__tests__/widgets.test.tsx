import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { WidgetCard } from '../WidgetCard'
import { ChartDataTable } from '../ChartDataTable'

describe('WidgetCard', () => {
  it('shows the failure state with Retry, and never the empty copy, when a query failed', async () => {
    const onRetry = vi.fn()
    render(
      <WidgetCard title="Lifecycle" isError isEmpty emptyLabel="Nothing here yet" onRetry={onRetry}>
        <p>rows</p>
      </WidgetCard>,
    )
    expect(screen.queryByText('Nothing here yet')).not.toBeInTheDocument()
    expect(screen.queryByText('rows')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('prefers the failure state over the loading state', () => {
    render(<WidgetCard title="Lifecycle" isLoading isError onRetry={vi.fn()}><p>rows</p></WidgetCard>)
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })

  it('renders children only when the query succeeded and is non-empty', () => {
    render(<WidgetCard title="Lifecycle"><p>rows</p></WidgetCard>)
    expect(screen.getByText('rows')).toBeInTheDocument()
  })

  it('labels its region with the title so the page has a navigable outline', () => {
    render(<WidgetCard title="File types"><p>rows</p></WidgetCard>)
    expect(screen.getByRole('region', { name: 'File types' })).toBeInTheDocument()
  })
})

describe('ChartDataTable', () => {
  it('exposes the chart numbers to assistive tech', () => {
    render(
      <ChartDataTable
        caption="Documents added per month"
        columns={['Month', 'Documents']}
        rows={[{ key: 'jan', label: 'Jan', value: 12 }]}
      />,
    )
    const table = screen.getByRole('table', { name: 'Documents added per month' })
    expect(table).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: 'Jan' })).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: '12' })).toBeInTheDocument()
  })
})
