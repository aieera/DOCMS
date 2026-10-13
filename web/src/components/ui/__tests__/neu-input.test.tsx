import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { Input } from '@/components/ui/shadcn/input'
describe('neumorphic Input', () => {
  it('is an inset well with a real border and a focus ring', () => {
    const { getByRole } = render(<Input aria-label="x" />)
    const c = getByRole('textbox').className
    expect(c).toContain('shadow-neu-inset')
    expect(c).toContain('border-input')
    expect(c).toContain('focus-visible:ring-2')
  })
})
