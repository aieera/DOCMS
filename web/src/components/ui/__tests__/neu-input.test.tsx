import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { Input } from '@/components/ui/shadcn/input'
import { Textarea } from '@/components/ui/shadcn/textarea'

// Historical filename (these fields were neumorphic inset wells until the
// 2026-10 design system). The assertions now pin the flat outline surface.
const HALO = 'focus-visible:shadow-[0_0_0_4px_color-mix(in_srgb,var(--ring)_50%,transparent)]'

describe('flat Input', () => {
  it('is a 36px outline-surface field with a real border and a focus ring + halo', () => {
    const { getByRole } = render(<Input aria-label="x" />)
    const el = getByRole('textbox')
    expect(el).toHaveClass('h-9', 'rounded-md', 'border', 'border-input', 'bg-outline-bg')
    expect(el).toHaveClass('focus-visible:ring-1', 'focus-visible:ring-ring')
    expect(el.className).toContain(HALO)
    expect(el.className).not.toMatch(/shadow-neu/)
  })

  it('flips the border and halo to destructive when invalid', () => {
    const { getByRole } = render(<Input aria-label="x" error="Required" />)
    const el = getByRole('textbox')
    expect(el).toHaveAttribute('aria-invalid', 'true')
    expect(el).toHaveClass('aria-[invalid=true]:border-destructive')
    expect(el.className).toContain(
      'aria-[invalid=true]:focus-visible:shadow-[0_0_0_4px_color-mix(in_srgb,var(--destructive)_50%,transparent)]',
    )
  })

  it('keeps the leading-icon start padding', () => {
    const { getByRole } = render(<Input aria-label="x" icon={<svg />} />)
    expect(getByRole('textbox')).toHaveClass('ps-9')
  })
})

describe('flat Textarea', () => {
  it('shares the field surface and has a 5rem minimum height', () => {
    const { getByRole } = render(<Textarea aria-label="x" />)
    const el = getByRole('textbox')
    expect(el).toHaveClass('min-h-[5rem]', 'rounded-md', 'border-input', 'bg-outline-bg')
    expect(el).toHaveClass('focus-visible:ring-1', 'focus-visible:ring-ring')
    expect(el.className).toContain(HALO)
    expect(el.className).not.toMatch(/shadow-neu|min-h-\[60px\]/)
  })
})
