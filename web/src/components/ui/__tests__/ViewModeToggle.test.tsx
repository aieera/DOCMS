import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ViewModeToggle } from '@/components/ui/ViewModeToggle'

describe('<ViewModeToggle>', () => {
  it('in grid mode, offers switching to list', async () => {
    const user = userEvent.setup()
    const spy = vi.fn()
    render(<ViewModeToggle value="grid" onChange={spy} />)
    const btn = screen.getByRole('button', { name: 'Switch to list view' })
    await user.click(btn)
    expect(spy).toHaveBeenCalledWith('list')
  })

  it('in list mode, offers switching to grid', async () => {
    const user = userEvent.setup()
    const spy = vi.fn()
    render(<ViewModeToggle value="list" onChange={spy} />)
    const btn = screen.getByRole('button', { name: 'Switch to grid view' })
    await user.click(btn)
    expect(spy).toHaveBeenCalledWith('grid')
  })

  it('renders a single button', () => {
    render(<ViewModeToggle value="grid" onChange={() => {}} />)
    expect(screen.getAllByRole('button')).toHaveLength(1)
  })
})
