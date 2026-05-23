import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'

import { TypedConfirmDialog } from '@/components/ui/shadcn/typed-confirm-dialog'

// TypedConfirmDialog is the high-risk-action variant of ConfirmDialog —
// the confirm button stays disabled until the user types the exact
// expected value (case + whitespace sensitive). These tests pin the
// gate so future refactors can't accidentally accept a confirm with a
// near-match.

describe('<TypedConfirmDialog>', () => {
  it('renders the expected value inline so the user knows what to type', () => {
    render(
      <TypedConfirmDialog
        open
        onOpenChange={() => {}}
        title="Delete workspace?"
        description="Soft-deletes the workspace."
        expectedValue="Sales reports"
        onConfirm={() => {}}
      />,
    )
    const alert = screen.getByRole('alertdialog')
    expect(within(alert).getByText('Sales reports')).toBeInTheDocument()
  })

  it('keeps confirm disabled until the typed value matches exactly', () => {
    const onConfirm = vi.fn()
    render(
      <TypedConfirmDialog
        open
        onOpenChange={() => {}}
        title="x"
        description="d"
        expectedValue="ACME Holdings"
        confirmLabel="Delete"
        destructive
        onConfirm={onConfirm}
      />,
    )
    const confirm = screen.getByTestId('typed-confirm-confirm')
    const input = screen.getByTestId('typed-confirm-input') as HTMLInputElement
    expect(confirm).toBeDisabled()
    // Direct value writes (fireEvent.change) instead of userEvent.type
    // — three keystroke-by-keystroke type calls would exceed the 5s
    // vitest default timeout under suite-wide load and aren't part of
    // what the test pins (the *resulting* value, not the typing
    // motion, is what gates confirm).
    fireEvent.change(input, { target: { value: 'acme holdings' } }) // wrong case
    expect(confirm).toBeDisabled()
    fireEvent.change(input, { target: { value: 'ACME Holdings ' } }) // trailing space
    expect(confirm).toBeDisabled()
    fireEvent.change(input, { target: { value: 'ACME Holdings' } }) // exact
    expect(confirm).not.toBeDisabled()
    fireEvent.click(confirm)
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('clears the typed value each time the dialog re-opens', () => {
    const { rerender } = render(
      <TypedConfirmDialog
        open
        onOpenChange={() => {}}
        title="x"
        description="d"
        expectedValue="repo-x"
        onConfirm={() => {}}
      />,
    )
    const input1 = screen.getByTestId('typed-confirm-input') as HTMLInputElement
    fireEvent.change(input1, { target: { value: 'repo-x' } })
    expect(input1.value).toBe('repo-x')

    rerender(
      <TypedConfirmDialog
        open={false}
        onOpenChange={() => {}}
        title="x"
        description="d"
        expectedValue="repo-x"
        onConfirm={() => {}}
      />,
    )
    rerender(
      <TypedConfirmDialog
        open
        onOpenChange={() => {}}
        title="x"
        description="d"
        expectedValue="repo-x"
        onConfirm={() => {}}
      />,
    )
    const input2 = screen.getByTestId('typed-confirm-input') as HTMLInputElement
    expect(input2.value).toBe('')
    expect(screen.getByTestId('typed-confirm-confirm')).toBeDisabled()
  })

  it('shows a spinner and stays disabled while loading=true', () => {
    render(
      <TypedConfirmDialog
        open
        onOpenChange={() => {}}
        title="x"
        description="d"
        expectedValue="match"
        loading
        onConfirm={() => {}}
      />,
    )
    const input = screen.getByTestId('typed-confirm-input') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'match' } })
    expect(screen.getByTestId('typed-confirm-confirm')).toBeDisabled()
  })
})
