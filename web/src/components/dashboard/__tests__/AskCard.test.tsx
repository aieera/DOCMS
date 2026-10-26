import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'

import { AskCard } from '../AskCard'
import { MAX_QUESTION_LEN } from '@/lib/askLimits'
import arCommon from '../../../../public/locales/ar/common.json'

const navigate = vi.fn()
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useNavigate: () => navigate,
}))

const box = () => screen.getByRole('textbox', { name: 'Ask a question' })

beforeEach(() => {
  navigate.mockReset()
})

afterEach(async () => {
  await act(async () => { await i18n.changeLanguage('en') })
})

describe('AskCard', () => {
  it('renders the card chrome: title, subtitle, labelled box, hint and Ask button', () => {
    render(<AskCard />)
    expect(screen.getByRole('heading', { level: 2, name: 'Ask a question' })).toBeInTheDocument()
    expect(screen.getByText('Get answers from the documents you can see')).toBeInTheDocument()
    expect(box()).toHaveAttribute('placeholder', 'e.g. Which documents are still in draft?')
    expect(box()).toHaveAttribute('rows', '3')
    expect(screen.getByText('Press Enter to ask')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Ask' })).toBeInTheDocument()
  })

  it('caps the box at the Ask page limit', () => {
    render(<AskCard />)
    expect(MAX_QUESTION_LEN).toBe(4000)
    expect(box()).toHaveAttribute('maxlength', String(MAX_QUESTION_LEN))
  })

  it('Enter sends the question to /ask?q=', async () => {
    const user = userEvent.setup()
    render(<AskCard />)
    await user.type(box(), 'draft?{Enter}')
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(navigate).toHaveBeenCalledWith({ to: '/ask', search: { q: 'draft?' } })
  })

  it('the Ask button sends the trimmed question', async () => {
    const user = userEvent.setup()
    render(<AskCard />)
    await user.type(box(), '  which are drafts?  ')
    await user.click(screen.getByRole('button', { name: 'Ask' }))
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(navigate).toHaveBeenCalledWith({ to: '/ask', search: { q: 'which are drafts?' } })
  })

  it('does not navigate for an empty or whitespace-only question', async () => {
    const user = userEvent.setup()
    render(<AskCard />)
    expect(screen.getByRole('button', { name: 'Ask' })).toBeDisabled()
    await user.type(box(), '   {Enter}')
    expect(navigate).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Ask' })).toBeDisabled()
    // Enter on an empty box is still a no-op, not a stray newline.
    await user.clear(box())
    await user.type(box(), '{Enter}')
    expect(navigate).not.toHaveBeenCalled()
    expect(box()).toHaveValue('')
  })

  it('Shift+Enter inserts a newline and does not navigate', async () => {
    const user = userEvent.setup()
    render(<AskCard />)
    await user.type(box(), 'first{Shift>}{Enter}{/Shift}second')
    expect(box()).toHaveValue('first\nsecond')
    expect(navigate).not.toHaveBeenCalled()
  })

  it('ignores the Enter that commits an IME composition', async () => {
    const user = userEvent.setup()
    render(<AskCard />)
    await user.type(box(), 'مرحبا')
    // Arabic / CJK input methods use Enter to accept the composition.
    fireEvent.keyDown(box(), { key: 'Enter', isComposing: true })
    expect(navigate).not.toHaveBeenCalled()
    expect(box()).toHaveValue('مرحبا')
    // The next, real Enter sends it.
    fireEvent.keyDown(box(), { key: 'Enter', isComposing: false })
    expect(navigate).toHaveBeenCalledWith({ to: '/ask', search: { q: 'مرحبا' } })
  })

  it('reads in Arabic', async () => {
    i18n.addResourceBundle('ar', 'common', arCommon, true, true)
    await act(async () => { await i18n.changeLanguage('ar') })
    render(<AskCard />)
    expect(screen.getByRole('heading', { level: 2, name: 'اطرح سؤالاً' })).toBeInTheDocument()
    expect(screen.getByText('احصل على إجابات من المستندات التي يمكنك رؤيتها')).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'اطرح سؤالاً' })).toHaveAttribute(
      'placeholder', 'مثال: ما المستندات التي لا تزال مسودة؟',
    )
    expect(screen.getByText('اضغط Enter للسؤال')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'اسأل' })).toBeInTheDocument()
  })
})
