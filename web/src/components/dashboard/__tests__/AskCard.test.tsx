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

const box = () => screen.getByRole('textbox', { name: 'Your question' })

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

  it('shows a visible label tied to the box, not a hidden one', () => {
    render(<AskCard />)
    const label = screen.getByText('Your question')
    expect(label.tagName).toBe('LABEL')
    expect(label).toHaveAttribute('for', box().id)
    expect(label).toHaveClass('text-sm', 'font-medium')
    expect(label).not.toHaveClass('sr-only')
    // The label sits above the box.
    expect(label.compareDocumentPosition(box()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('puts the icon tile at the start of the header, before the title', () => {
    render(<AskCard />)
    const tile = screen.getByTestId('ask-card-icon')
    const title = screen.getByRole('heading', { level: 2, name: 'Ask a question' })
    expect(tile).toHaveAttribute('aria-hidden', 'true')
    expect(tile).toHaveClass('h-8', 'w-8', 'rounded-md', 'bg-muted')
    expect(tile.closest('header')).toBe(title.closest('header'))
    expect(tile.compareDocumentPosition(title) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('lets the browser pick the text direction and pins the footer to the card bottom', () => {
    render(<AskCard />)
    expect(box()).toHaveAttribute('dir', 'auto')
    expect(screen.getByText('Press Enter to ask').parentElement).toHaveClass('mt-auto')
  })

  it('caps the box at the Ask page limit', () => {
    render(<AskCard />)
    expect(MAX_QUESTION_LEN).toBe(4000)
    expect(box()).toHaveAttribute('maxlength', String(MAX_QUESTION_LEN))
  })

  it('Enter hands the question to /ask in history state, never in the URL', async () => {
    const user = userEvent.setup()
    render(<AskCard />)
    await user.type(box(), 'draft?{Enter}')
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(navigate).toHaveBeenCalledWith({ to: '/ask', state: { askQuestion: 'draft?' } })
    // No `search`: a question in the URL is one a crafted link could also carry.
    expect(navigate.mock.calls[0][0]).not.toHaveProperty('search')
  })

  it('the Ask button sends the trimmed question', async () => {
    const user = userEvent.setup()
    render(<AskCard />)
    await user.type(box(), '  which are drafts?  ')
    await user.click(screen.getByRole('button', { name: 'Ask' }))
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(navigate).toHaveBeenCalledWith({ to: '/ask', state: { askQuestion: 'which are drafts?' } })
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
    await user.type(box(), 'こんにちは')
    // Composing input methods (Japanese, Chinese, Korean, Indic and
    // transliteration keyboards) use Enter to accept the candidate text.
    // Chromium and Firefox flag that keydown with isComposing.
    fireEvent.keyDown(box(), { key: 'Enter', isComposing: true })
    expect(navigate).not.toHaveBeenCalled()
    expect(box()).toHaveValue('こんにちは')
    // The next, real Enter sends it.
    fireEvent.keyDown(box(), { key: 'Enter', isComposing: false })
    expect(navigate).toHaveBeenCalledWith({ to: '/ask', state: { askQuestion: 'こんにちは' } })
  })

  it('ignores the committing Enter as Safari reports it (keyCode 229, isComposing false)', async () => {
    const user = userEvent.setup()
    render(<AskCard />)
    await user.type(box(), 'こんにちは')
    // Safari fires compositionend BEFORE keydown, so the Enter that commits
    // the composition arrives with isComposing already false.
    fireEvent.keyDown(box(), { key: 'Enter', keyCode: 229, isComposing: false })
    expect(navigate).not.toHaveBeenCalled()
    expect(box()).toHaveValue('こんにちは')
    // An ordinary Enter (keyCode 13) still sends.
    fireEvent.keyDown(box(), { key: 'Enter', keyCode: 13, isComposing: false })
    expect(navigate).toHaveBeenCalledTimes(1)
  })

  it('reads in Arabic', async () => {
    i18n.addResourceBundle('ar', 'common', arCommon, true, true)
    await act(async () => { await i18n.changeLanguage('ar') })
    render(<AskCard />)
    expect(screen.getByRole('heading', { level: 2, name: 'اطرح سؤالاً' })).toBeInTheDocument()
    expect(screen.getByText('احصل على إجابات من المستندات التي يمكنك رؤيتها')).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'سؤالك' })).toHaveAttribute(
      'placeholder', 'مثال: ما المستندات التي لا تزال مسودة؟',
    )
    expect(screen.getByText('اضغط Enter للسؤال')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'اسأل' })).toBeInTheDocument()
  })
})
