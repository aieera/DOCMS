import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { Button } from '@/components/ui/shadcn/button'
import { Badge } from '@/components/ui/shadcn/badge'
import {
  Card,
  CardContent,
  CardFooter,
  CardHeader,
} from '@/components/ui/card'

// The Button/Card/Badge surfaces were neumorphic until the 2026-10 design
// system; these assertions pin the flat equivalents.
describe('flat Button/Card/Badge surfaces', () => {
  it('default button is a flat accent surface with no neumorphic shadow', () => {
    const { getByRole } = render(<Button>Go</Button>)
    const c = getByRole('button').className
    expect(c).toContain('bg-primary')
    expect(c).not.toMatch(/shadow-neu|brightness/)
    expect(c).not.toContain('active:shadow')
  })

  it('card is a bordered surface at the 14px radius with the sm shadow', () => {
    const { container } = render(<Card>x</Card>)
    const el = container.firstChild as HTMLElement
    expect(el).toHaveClass('rounded-xl', 'border', 'border-border', 'bg-card', 'shadow-sm')
    expect(el.className).not.toContain('shadow-neu')
  })

  it('card header, content and footer use the 20px padding scale', () => {
    const { getByTestId } = render(
      <Card>
        <CardHeader data-testid="h" />
        <CardContent data-testid="c" />
        <CardFooter data-testid="f" />
      </Card>,
    )
    expect(getByTestId('h')).toHaveClass('p-5', 'pb-4')
    expect(getByTestId('c')).toHaveClass('p-5', 'pt-0')
    expect(getByTestId('f')).toHaveClass('p-5', 'pt-0')
  })

  it('count badge is an 18px primary pill', () => {
    const { getByText } = render(<Badge variant="count">3</Badge>)
    expect(getByText('3')).toHaveClass(
      'h-[18px]',
      'min-w-[18px]',
      'rounded-full',
      'bg-primary',
      'px-1',
      'text-[11px]',
      'text-primary-foreground',
    )
  })

  it('badge is a span, so it is valid phrasing content inside a button', () => {
    const { getByRole, getByText } = render(
      <Button>
        Tasks <Badge variant="count">3</Badge>
      </Button>,
    )
    const badge = getByText('3')
    expect(badge.tagName).toBe('SPAN')
    expect(getByRole('button')).toContainElement(badge)
  })

  it('badge focus uses the shared halo token', () => {
    const { getByText } = render(<Badge>b</Badge>)
    expect(getByText('b')).toHaveClass('focus-visible:ring-1', 'focus-visible:shadow-focus-halo')
  })

  it('no badge variant carries a neumorphic shadow', () => {
    const variants = [
      'default', 'secondary', 'destructive', 'outline', 'draft', 'in_review', 'active',
      'superseded', 'archived', 'disposed', 'info', 'success', 'warning', 'count',
    ]
    for (const variant of variants) {
      const { getByText, unmount } = render(<Badge variant={variant}>{variant}</Badge>)
      expect(getByText(variant).className).not.toMatch(/shadow-neu/)
      unmount()
    }
  })

  it('lifecycle badge variants keep their tinted colours', () => {
    const { getByText } = render(
      <>
        <Badge variant="in_review">r</Badge>
        <Badge variant="active">a</Badge>
        <Badge variant="disposed">d</Badge>
      </>,
    )
    expect(getByText('r')).toHaveClass('bg-warning/15', 'text-warning-strong')
    expect(getByText('a')).toHaveClass('bg-success/8', 'text-success')
    expect(getByText('d')).toHaveClass('bg-destructive/15', 'text-foreground')
  })
})
