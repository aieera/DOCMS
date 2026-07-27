import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { PasswordInput } from '@/components/ui/PasswordInput'

describe('<PasswordInput>', () => {
  it('renders a password input by default', () => {
    render(<PasswordInput aria-label="Password" />)
    expect(screen.getByLabelText('Password')).toHaveAttribute('type', 'password')
  })

  it('toggle reveals and re-hides the value', async () => {
    const user = userEvent.setup()
    render(<PasswordInput aria-label="Password" />)
    const input = screen.getByLabelText('Password')
    await user.click(screen.getByRole('button', { name: 'Show password' }))
    expect(input).toHaveAttribute('type', 'text')
    await user.click(screen.getByRole('button', { name: 'Hide password' }))
    expect(input).toHaveAttribute('type', 'password')
  })

  it('keeps the typed value across a toggle', async () => {
    const user = userEvent.setup()
    render(<PasswordInput aria-label="Password" />)
    const input = screen.getByLabelText('Password')
    await user.type(input, 'hunter2')
    await user.click(screen.getByRole('button', { name: 'Show password' }))
    expect(input).toHaveValue('hunter2')
  })

  it('disables the toggle when the input is disabled', () => {
    render(<PasswordInput aria-label="Password" disabled />)
    expect(screen.getByRole('button', { name: 'Show password' })).toBeDisabled()
  })
})
