import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { RenameDocumentDialog } from '../RenameDocumentDialog'

const mutate = vi.fn()
vi.mock('@/hooks/useDocuments', () => ({
  useUpdateDocument: () => ({ mutate, isPending: false }),
}))

const toastError = vi.fn()
vi.mock('sonner', () => ({ toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn() } }))

beforeEach(() => {
  mutate.mockReset()
  toastError.mockReset()
})

function open() {
  return render(
    <RenameDocumentDialog
      open
      onOpenChange={() => {}}
      documentId="doc-1"
      initialTitle="Acme MSA 2026"
    />,
  )
}

// Validation used to be toast-only: Save stayed enabled, clicking it
// fired a bottom-right toast and nothing else. The New-folder dialog's
// pattern (disabled submit) is the house style.
describe('RenameDocumentDialog validation', () => {
  it('disables Save while the title is unchanged', () => {
    open()
    expect(screen.getByTestId('rename-document-submit')).toBeDisabled()
  })

  it('disables Save and explains why when the title is emptied', async () => {
    open()
    const input = screen.getByTestId('rename-document-input')
    await userEvent.clear(input)

    expect(screen.getByTestId('rename-document-submit')).toBeDisabled()
    expect(input).toHaveAttribute('aria-invalid', 'true')

    // The reason is inline and wired to the field, not a transient toast.
    const message = screen.getByText('Enter a title.')
    expect(input.getAttribute('aria-describedby')).toContain(message.id)
    expect(toastError).not.toHaveBeenCalled()
  })

  it('enables Save once a different, non-empty title is typed', async () => {
    open()
    const input = screen.getByTestId('rename-document-input')
    await userEvent.clear(input)
    await userEvent.type(input, 'Acme MSA 2027')

    const submit = screen.getByTestId('rename-document-submit')
    expect(submit).toBeEnabled()
    expect(input).not.toHaveAttribute('aria-invalid')

    await userEvent.click(submit)
    expect(mutate).toHaveBeenCalledWith(
      { id: 'doc-1', body: { title: 'Acme MSA 2027' } },
      expect.anything(),
    )
  })

  it('trims whitespace before deciding the title changed', async () => {
    open()
    const input = screen.getByTestId('rename-document-input')
    await userEvent.type(input, '   ')
    expect(screen.getByTestId('rename-document-submit')).toBeDisabled()
  })
})
