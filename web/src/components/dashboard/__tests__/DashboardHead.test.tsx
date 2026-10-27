import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n, { type TFunction } from 'i18next'

import { DashboardHead } from '../DashboardHead'
import { greet } from '../greet'
import enCommon from '../../../../public/locales/en/common.json'
import arCommon from '../../../../public/locales/ar/common.json'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, ...rest }: { children: React.ReactNode; to?: string } & Record<string, unknown>) =>
    <a href={to} {...rest}>{children}</a>,
}))

const t = i18n.getFixedT('en', 'common') as unknown as TFunction<'common'>

describe('greet', () => {
  beforeEach(() => {
    // Only Date is faked, so nothing else in the run waits on fake timers.
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 9, 26, 9, 0, 0))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('reads "Good morning" with no comma and no "undefined" when there is no name', () => {
    expect(greet(t, undefined)).toBe('Good morning')
  })

  it('treats an empty first name like no name', () => {
    expect(greet(t, '')).toBe('Good morning')
  })

  it('appends the first name after the comma', () => {
    expect(greet(t, 'Sulthan')).toBe('Good morning, Sulthan')
  })
})

describe('DashboardHead', () => {
  it('renders the greeting as the page h1', () => {
    render(<DashboardHead greeting="Hi" workspaceCount={2} onUpload={vi.fn()} />)
    const h1 = screen.getByRole('heading', { level: 1, name: 'Hi' })
    expect(h1).toHaveClass('page-title')
  })

  it('uses the plural form for zero workspaces', () => {
    render(<DashboardHead greeting="Hi" workspaceCount={0} onUpload={vi.fn()} />)
    expect(screen.getByText("Here's what's happening across your 0 workspaces.")).toBeInTheDocument()
  })

  it('uses the singular form for one workspace', () => {
    render(<DashboardHead greeting="Hi" workspaceCount={1} onUpload={vi.fn()} />)
    expect(screen.getByText("Here's what's happening across your 1 workspace.")).toBeInTheDocument()
  })

  it('renders no workspace count while the count is still unknown', () => {
    render(<DashboardHead greeting="Hi" workspaceCount={undefined} onUpload={vi.fn()} />)
    expect(screen.queryByText(/across your/)).not.toBeInTheDocument()
  })

  it('links "Ask a question" to /ask as an outline button', () => {
    render(<DashboardHead greeting="Hi" workspaceCount={1} onUpload={vi.fn()} />)
    const ask = screen.getByRole('link', { name: 'Ask a question' })
    expect(ask).toHaveAttribute('href', '/ask')
    expect(ask).toHaveClass('border', 'bg-outline-bg')
  })

  it('calls onUpload from the primary Upload button', async () => {
    const onUpload = vi.fn()
    render(<DashboardHead greeting="Hi" workspaceCount={1} onUpload={onUpload} />)
    const upload = screen.getByTestId('header-upload-button')
    expect(upload).toHaveAccessibleName('Upload')
    expect(upload).toHaveClass('bg-primary')
    await userEvent.click(upload)
    expect(onUpload).toHaveBeenCalledTimes(1)
  })
})

describe('DashboardHead file drop', () => {
  const file = (name: string) => new File(['x'], name, { type: 'application/pdf' })
  const filesDrag = (files: File[] = []) => ({ dataTransfer: { types: ['Files'], files } })
  const upload = () => screen.getByTestId('header-upload-button')
  const renderHead = (onFilesDropped?: (files: File[]) => void, onUpload = vi.fn()) =>
    render(<DashboardHead greeting="Hi" workspaceCount={1} onUpload={onUpload} onFilesDropped={onFilesDropped} />)

  it('hands every dropped file to onFilesDropped', () => {
    const onFilesDropped = vi.fn()
    renderHead(onFilesDropped)
    const files = [file('a.pdf'), file('b.pdf')]
    fireEvent.drop(upload(), filesDrag(files))
    expect(onFilesDropped).toHaveBeenCalledTimes(1)
    expect(onFilesDropped).toHaveBeenCalledWith(files)
  })

  it('ignores a drop that carries no files, but still stops the browser from handling it', () => {
    const onFilesDropped = vi.fn()
    renderHead(onFilesDropped)
    // fireEvent returns false when the event was cancelled with preventDefault.
    expect(fireEvent.drop(upload(), filesDrag([]))).toBe(false)
    expect(onFilesDropped).not.toHaveBeenCalled()
  })

  it('accepts a file drag over the button: dragenter and dragover are cancelled', () => {
    renderHead(vi.fn())
    expect(fireEvent.dragEnter(upload(), filesDrag())).toBe(false)
    expect(fireEvent.dragOver(upload(), filesDrag())).toBe(false)
  })

  it('does not accept a drag of anything but files (text, a link)', () => {
    renderHead(vi.fn())
    const text = { dataTransfer: { types: ['text/plain'], files: [] } }
    expect(fireEvent.dragEnter(upload(), text)).toBe(true)
    expect(fireEvent.dragOver(upload(), text)).toBe(true)
    expect(upload()).not.toHaveClass('ring-2')
  })

  it('highlights the button while a file is dragged over it and clears the highlight on dragleave', () => {
    renderHead(vi.fn())
    expect(upload()).not.toHaveClass('ring-2')
    fireEvent.dragEnter(upload(), filesDrag())
    expect(upload()).toHaveClass('ring-2', 'ring-ring')
    fireEvent.dragLeave(upload(), filesDrag())
    expect(upload()).not.toHaveClass('ring-2', 'ring-ring')
  })

  it('clears the highlight when the files are dropped', () => {
    renderHead(vi.fn())
    fireEvent.dragOver(upload(), filesDrag())
    expect(upload()).toHaveClass('ring-2')
    fireEvent.drop(upload(), filesDrag([file('a.pdf')]))
    expect(upload()).not.toHaveClass('ring-2')
  })

  it('is not a drop target without onFilesDropped: nothing is cancelled and nothing highlights', () => {
    renderHead(undefined)
    expect(fireEvent.dragEnter(upload(), filesDrag())).toBe(true)
    expect(fireEvent.dragOver(upload(), filesDrag())).toBe(true)
    expect(fireEvent.drop(upload(), filesDrag([file('a.pdf')]))).toBe(true)
    expect(upload()).not.toHaveClass('ring-2')
  })

  it('still calls onUpload on click and not on a drop', async () => {
    const onUpload = vi.fn()
    renderHead(vi.fn(), onUpload)
    fireEvent.drop(upload(), filesDrag([file('a.pdf')]))
    expect(onUpload).not.toHaveBeenCalled()
    await userEvent.click(upload())
    expect(onUpload).toHaveBeenCalledTimes(1)
  })
})

describe('dashboard head copy', () => {
  type Catalogue = { dashboard: Record<string, unknown> }
  const en = (enCommon as unknown as Catalogue).dashboard
  const ar = (arCommon as unknown as Catalogue).dashboard

  it('ships the subline and the ask action in English', () => {
    expect(en.subline_one).toBe("Here's what's happening across your {{count}} workspace.")
    expect(en.subline_other).toBe("Here's what's happening across your {{count}} workspaces.")
    expect(en.ask_question).toBe('Ask a question')
  })

  it('ships every Arabic plural form of the subline, so no count falls back to English', () => {
    expect(ar.subline_one).toBe('إليك ما يحدث في مساحة العمل {{count}} الخاصة بك.')
    expect(ar.subline_other).toBe('إليك ما يحدث عبر مساحات العمل {{count}} الخاصة بك.')
    for (const form of ['zero', 'two', 'few', 'many']) {
      expect(ar[`subline_${form}`], `subline_${form}`).toEqual(expect.any(String))
    }
    expect(ar.ask_question).toBe('اطرح سؤالاً')
  })
})
