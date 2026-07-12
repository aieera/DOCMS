import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'

import { AnswerMarkdown } from '../AnswerMarkdown'

const DOC_A = 'aaaaaaaa-1111-2222-3333-444444444444'
const DOC_B = 'bbbbbbbb-1111-2222-3333-444444444444'

describe('AnswerMarkdown', () => {
  it('renders Markdown instead of raw syntax (the `**1 workspace**` bug)', () => {
    const { container } = render(<AnswerMarkdown text={'There is **1 workspace** available.'} />)
    expect(container.querySelector('strong')?.textContent).toBe('1 workspace')
    expect(container.textContent).not.toContain('**')
  })

  it('renders GFM lists', () => {
    const { container } = render(<AnswerMarkdown text={'- alpha\n- beta'} />)
    const items = Array.from(container.querySelectorAll('li')).map((li) => li.textContent)
    expect(items).toEqual(['alpha', 'beta'])
  })

  it('swaps known citation tokens for the chip', () => {
    const { container } = render(
      <AnswerMarkdown
        text={`Rent is due monthly [${DOC_A}:page_3].`}
        isCitation={(id) => id === DOC_A}
        renderCitation={(token) => <span data-testid="chip">{token}</span>}
      />,
    )
    expect(container.querySelector('[data-testid="chip"]')?.textContent).toBe(`${DOC_A}:page_3`)
    expect(container.textContent).not.toContain(`[${DOC_A}`)
  })

  it('keeps unknown citation tokens as raw text (no broken links)', () => {
    const { container } = render(
      <AnswerMarkdown
        text={`See [${DOC_B}:page_1] for details.`}
        isCitation={() => false}
        renderCitation={() => <span data-testid="chip" />}
      />,
    )
    expect(container.querySelector('[data-testid="chip"]')).toBeNull()
    expect(container.textContent).toContain(`[${DOC_B}:page_1]`)
    expect(container.querySelector('a')).toBeNull()
  })

  it('collapses adjacent duplicate chips and tightens punctuation', () => {
    const { container } = render(
      <AnswerMarkdown
        text={`Stated twice [${DOC_A}:page_2] [${DOC_A}:page_2] .`}
        isCitation={() => true}
        renderCitation={() => <span data-testid="chip">†</span>}
      />,
    )
    expect(container.querySelectorAll('[data-testid="chip"]')).toHaveLength(1)
    // The stray space the model leaves between the chip and the period
    // is tightened: "† ." → "†.".
    expect(container.textContent).toContain('†.')
    expect(container.textContent).not.toContain('† .')
  })

  it('renders citations inside Markdown structures like bold text', () => {
    const { container } = render(
      <AnswerMarkdown
        text={`**Key term** is defined in [${DOC_A}].`}
        isCitation={() => true}
        renderCitation={() => <span data-testid="chip" />}
      />,
    )
    expect(container.querySelector('strong')?.textContent).toBe('Key term')
    expect(container.querySelectorAll('[data-testid="chip"]')).toHaveLength(1)
  })

  it('keeps external links but forces new-tab + noopener', () => {
    const { container } = render(<AnswerMarkdown text={'[docs](https://example.com)'} />)
    const a = container.querySelector('a')
    expect(a?.getAttribute('href')).toBe('https://example.com')
    expect(a?.getAttribute('target')).toBe('_blank')
    expect(a?.getAttribute('rel')).toContain('noopener')
  })

  it('does not render raw HTML from the model', () => {
    const { container } = render(<AnswerMarkdown text={'<img src=x onerror=alert(1)>hi'} />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toContain('hi')
  })
})
