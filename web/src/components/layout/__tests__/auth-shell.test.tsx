// The sign-in shell (restyled 2026-10-27 to the app's design system: one
// centred column, the brand mark above a bordered card, nothing animated).
// These pin the structure the four unauthenticated routes and the e2e
// specs rely on — the card's test id, the h1, the footer inside the card,
// the brand link back to sign-in — and that the old animated stage is gone.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'

import { AuthShell } from '../auth-shell'

// The shell renders the brand mark as a router Link; without a router
// context it throws on `isServer`.
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, ...rest }: { children: React.ReactNode; to?: string } & Record<string, unknown>) =>
    <a href={to} {...rest}>{children}</a>,
}))

function card(container: HTMLElement) {
  return container.querySelector('[data-testid="auth-card"]') as HTMLElement
}

describe('AuthShell', () => {
  it('renders the heading, description, children and footer inside the card', () => {
    const { container } = render(
      <AuthShell title="Welcome back" description="Sign in to continue." footer={<span>New here?</span>}>
        <button type="button">Sign in</button>
      </AuthShell>,
    )
    const c = within(card(container))
    expect(c.getByRole('heading', { level: 1, name: 'Welcome back' })).toHaveClass('text-xl', 'font-semibold', 'tracking-tight')
    expect(c.getByText('Sign in to continue.')).toHaveClass('text-sm', 'text-muted-foreground')
    expect(c.getByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(c.getByText('New here?')).toBeInTheDocument()
  })

  it('puts the form on a bordered design-system card, 384px wide at most, in the main landmark', () => {
    const { container } = render(<AuthShell title="t"><span>form</span></AuthShell>)
    const el = card(container)
    expect(el).toBeTruthy()
    expect(el).toHaveClass('rounded-xl', 'border', 'border-border', 'bg-card', 'shadow-sm', 'p-6')
    expect(screen.getByRole('main')).toHaveClass('w-full', 'max-w-sm')
    expect(screen.getByRole('main')).toContainElement(el)
  })

  it('centres one column on the page background, full viewport height', () => {
    const { container } = render(<AuthShell title="t"><span>form</span></AuthShell>)
    expect(container.firstElementChild).toHaveClass(
      'min-h-dvh', 'flex', 'flex-col', 'items-center', 'justify-center', 'bg-background', 'p-4', 'shell:p-6',
    )
  })

  it('shows the brand mark above the card as a link back to sign-in', () => {
    const { container } = render(<AuthShell title="t"><span>form</span></AuthShell>)
    const brand = screen.getByRole('link', { name: 'SeDoc' })
    expect(brand).toHaveAttribute('href', '/login')
    // Same 32px primary tile as the sidebar's brand row.
    expect(brand.querySelector('.bg-primary')).toHaveClass('h-8', 'w-8', 'rounded-md', 'text-primary-foreground')
    expect(card(container)).not.toContainElement(brand)
    // Document order: brand first, then the card.
    expect(brand.compareDocumentPosition(card(container)) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('has no animated stage, pointer layer or entrance animation any more', () => {
    const { container } = render(<AuthShell title="t" stepKey="credentials"><span>form</span></AuthShell>)
    expect(container.querySelector('[data-testid="auth-stage"]')).toBeNull()
    expect(container.querySelector('[data-testid="pointer-fx"]')).toBeNull()
    expect(container.querySelector('aside')).toBeNull()
    expect(container.querySelector('canvas')).toBeNull()
    expect(container.querySelector('[data-motion]')).toBeNull()
    expect(card(container).className).not.toMatch(/auth-card-in/)
  })

  it('leaves the heading block and footer out when they are not given', () => {
    const { container } = render(<AuthShell><span>form</span></AuthShell>)
    expect(screen.queryByRole('heading')).toBeNull()
    // Only the children are inside the card.
    expect(card(container).children).toHaveLength(1)
  })

  it('re-mounts the card when the step changes, so each step starts fresh', () => {
    const { container, rerender } = render(
      <AuthShell title="t" stepKey="credentials"><span>form</span></AuthShell>,
    )
    const first = card(container)
    expect(first.getAttribute('data-step')).toBe('credentials')
    rerender(<AuthShell title="t" stepKey="mfa-verify"><span>form</span></AuthShell>)
    expect(card(container).getAttribute('data-step')).toBe('mfa-verify')
    expect(card(container)).not.toBe(first)
  })
})
