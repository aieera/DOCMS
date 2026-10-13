import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'

describe('neumorphic Button/Card', () => {
  it('default button is a raised neumorphic accent surface that presses in', () => {
    const { getByRole } = render(<Button>Go</Button>)
    const c = getByRole('button').className
    expect(c).toContain('shadow-neu')
    expect(c).toContain('active:shadow-neu-pressed')
    expect(c).toContain('bg-primary')
  })
  it('card is a raised neumorphic surface with no hard border', () => {
    const { container } = render(<Card>x</Card>)
    const c = (container.firstChild as HTMLElement).className
    expect(c).toContain('shadow-neu')
    expect(c).not.toMatch(/\bborder\b/)
  })
})
