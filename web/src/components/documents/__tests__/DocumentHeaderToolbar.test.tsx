import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DocumentHeaderToolbar } from '@/components/documents/DocumentHeaderToolbar'

// The compact replacement for the viewer's side-rail button stack:
// five primary actions as labeled icon buttons + the rare/destructive
// ones behind a "More actions" overflow menu.

function makeProps() {
  return {
    onDownload: vi.fn(),
    onShare: vi.fn(),
    onCreateTask: vi.fn(),
    onCompare: vi.fn(),
    onManageAccess: vi.fn(),
    onDeclareRecord: vi.fn(),
    onWormLock: vi.fn(),
    onVerifyIntegrity: vi.fn(),
    canDeclareRecord: true,
  }
}

describe('DocumentHeaderToolbar', () => {
  it('renders the five primary actions and fires their handlers', async () => {
    const p = makeProps()
    render(<DocumentHeaderToolbar {...p} />)

    for (const [name, fn] of [
      ['download', p.onDownload],
      ['share', p.onShare],
      ['create task', p.onCreateTask],
      ['compare', p.onCompare],
      ['manage access', p.onManageAccess],
    ] as const) {
      const btn = screen.getByRole('button', { name: new RegExp(name, 'i') })
      await userEvent.click(btn)
      expect(fn).toHaveBeenCalledTimes(1)
    }
  })

  it('puts record/WORM/integrity actions behind the overflow menu', async () => {
    const p = makeProps()
    render(<DocumentHeaderToolbar {...p} />)

    expect(screen.queryByText(/declare as record/i)).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /more actions/i }))

    await userEvent.click(await screen.findByText(/declare as record/i))
    expect(p.onDeclareRecord).toHaveBeenCalledTimes(1)
  })

  it('hides Declare as record when not permitted', async () => {
    const p = { ...makeProps(), canDeclareRecord: false }
    render(<DocumentHeaderToolbar {...p} />)

    await userEvent.click(screen.getByRole('button', { name: /more actions/i }))
    await screen.findByText(/worm lock/i)
    expect(screen.queryByText(/declare as record/i)).toBeNull()
  })

  it('renders no overflow menu when no secondary handlers are provided', () => {
    const { onDeclareRecord, onWormLock, onVerifyIntegrity, canDeclareRecord, ...primary } = makeProps()
    void onDeclareRecord
    void onWormLock
    void onVerifyIntegrity
    void canDeclareRecord
    render(<DocumentHeaderToolbar {...primary} />)
    expect(screen.queryByRole('button', { name: /more actions/i })).toBeNull()
  })

  it('disables Download when canDownload is false', () => {
    const p = { ...makeProps(), canDownload: false }
    render(<DocumentHeaderToolbar {...p} />)
    const btn = screen.getByRole('button', { name: /download/i }) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })
})
