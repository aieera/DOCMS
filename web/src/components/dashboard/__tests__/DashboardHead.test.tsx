import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
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
