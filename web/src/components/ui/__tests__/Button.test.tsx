import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Button } from '@/components/ui/Button'

describe('<Button>', () => {
  it('fires onClick when enabled', async () => {
    const user = userEvent.setup()
    const spy = vi.fn()
    render(<Button onClick={spy}>Save</Button>)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('is non-clickable when loading', async () => {
    const user = userEvent.setup()
    const spy = vi.fn()
    render(<Button loading onClick={spy}>Save</Button>)
    const btn = screen.getByRole('button')
    expect(btn).toBeDisabled()
    await user.click(btn)
    expect(spy).not.toHaveBeenCalled()
  })

  it('disabled prop blocks clicks', async () => {
    const user = userEvent.setup()
    const spy = vi.fn()
    render(<Button disabled onClick={spy}>Save</Button>)
    await user.click(screen.getByRole('button'))
    expect(spy).not.toHaveBeenCalled()
  })

  it('applies destructive variant class', () => {
    render(<Button variant="destructive">Delete</Button>)
    expect(screen.getByRole('button')).toHaveClass('bg-red-500')
  })

  it('forwards arbitrary className', () => {
    render(<Button className="my-extra-class">x</Button>)
    expect(screen.getByRole('button')).toHaveClass('my-extra-class')
  })
})
