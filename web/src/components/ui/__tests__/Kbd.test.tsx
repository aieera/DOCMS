import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { Kbd } from '@/components/ui/shadcn/kbd'

describe('<Kbd>', () => {
  it('renders a <kbd> element carrying its children', () => {
    render(<Kbd>Ctrl K</Kbd>)
    expect(screen.getByText('Ctrl K').tagName).toBe('KBD')
  })

  it('is a 22px muted chip with the 11px muted-foreground label', () => {
    render(<Kbd>Ctrl K</Kbd>)
    expect(screen.getByText('Ctrl K')).toHaveClass(
      'h-[22px]',
      'rounded-[4px]',
      'bg-muted',
      'px-1.5',
      'text-[11px]',
      'font-medium',
      'text-muted-foreground',
    )
  })

  it('forwards remaining HTML attributes onto the <kbd>', () => {
    render(<Kbd aria-hidden="true" data-testid="hint">⌘K</Kbd>)
    const el = screen.getByTestId('hint')
    expect(el.tagName).toBe('KBD')
    expect(el).toHaveAttribute('aria-hidden', 'true')
  })

  it('forwards an extra className without dropping the base classes', () => {
    render(<Kbd className="ms-auto">⌘K</Kbd>)
    expect(screen.getByText('⌘K')).toHaveClass('ms-auto', 'bg-muted')
  })
})
