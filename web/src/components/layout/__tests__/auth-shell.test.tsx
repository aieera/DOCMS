// The animated auth shell (2026-09-23). These pin the two things that
// decide whether a showpiece login is usable rather than merely pretty:
// it must go completely still when the viewer asks for reduced motion,
// and the pointer parallax must never engage on a touch device, where
// "pointer move" is a tap and the tilt would fight the scroll.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'

import { AuthShell } from '../auth-shell'
import { AuthHero } from '../AuthHero'

// jsdom has no matchMedia. Each test declares the two queries the shell
// asks about; anything else answers false.
function mockMedia(matches: Record<string, boolean>) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: Object.entries(matches).some(([frag, on]) => on && query.includes(frag)),
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
}

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, ...rest }: { children: React.ReactNode; to?: string } & Record<string, unknown>) =>
    <a href={to} {...rest}>{children}</a>,
}))

beforeEach(() => {
  mockMedia({ 'prefers-reduced-motion': false, 'pointer: coarse': false })
  // The tilt coalesces into one animation frame; run frames inline so the
  // assertions see the committed transform rather than a pending one.
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => { cb(0); return 1 })
  vi.stubGlobal('cancelAnimationFrame', vi.fn())
  // jsdom reports every element as 0x0, and the tilt correctly refuses to
  // divide by a zero-sized rect — give it a real one.
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue({
    width: 800, height: 600, left: 0, top: 0, right: 800, bottom: 600, x: 0, y: 0,
    toJSON: () => ({}),
  } as DOMRect)
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('AuthShell', () => {
  it('renders the heading, description, children and footer', () => {
    render(
      <AuthShell title="Welcome back" description="Sign in to continue." footer={<span>New here?</span>}>
        <button type="button">Sign in</button>
      </AuthShell>,
    )
    expect(screen.getByRole('heading', { name: 'Welcome back' })).toBeInTheDocument()
    expect(screen.getByText('Sign in to continue.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(screen.getByText('New here?')).toBeInTheDocument()
  })

  it('tilts the stage toward the pointer on a fine-pointer device', () => {
    const { container } = render(<AuthShell title="t"><span>form</span></AuthShell>)
    const stage = container.querySelector('[data-testid="auth-stage"]') as HTMLElement
    expect(stage).toBeTruthy()
    const before = stage.style.transform
    fireEvent.pointerMove(stage, { clientX: 20, clientY: 20 })
    expect(stage.style.transform).not.toBe(before)
    expect(stage.style.transform).toMatch(/rotate[XY]/)
  })

  it('returns the stage to rest when the pointer leaves', () => {
    const { container } = render(<AuthShell title="t"><span>form</span></AuthShell>)
    const stage = container.querySelector('[data-testid="auth-stage"]') as HTMLElement
    fireEvent.pointerMove(stage, { clientX: 20, clientY: 20 })
    fireEvent.pointerLeave(stage)
    expect(stage.style.transform).toMatch(/rotateX\(0(\.0+)?deg\) rotateY\(0(\.0+)?deg\)/)
  })

  it('never tilts on a coarse pointer — a tap is not a hover', () => {
    mockMedia({ 'prefers-reduced-motion': false, 'pointer: coarse': true })
    const { container } = render(<AuthShell title="t"><span>form</span></AuthShell>)
    const stage = container.querySelector('[data-testid="auth-stage"]') as HTMLElement
    fireEvent.pointerMove(stage, { clientX: 20, clientY: 20 })
    // Still at rest: any engaged tilt would put non-zero degrees here.
    expect(stage.style.transform).toMatch(/rotateX\(0(\.0+)?deg\) rotateY\(0(\.0+)?deg\)/)
  })

  it('never tilts when the viewer asked for reduced motion', () => {
    mockMedia({ 'prefers-reduced-motion': true, 'pointer: coarse': false })
    const { container } = render(<AuthShell title="t"><span>form</span></AuthShell>)
    const stage = container.querySelector('[data-testid="auth-stage"]') as HTMLElement
    fireEvent.pointerMove(stage, { clientX: 20, clientY: 20 })
    // Still at rest: any engaged tilt would put non-zero degrees here.
    expect(stage.style.transform).toMatch(/rotateX\(0(\.0+)?deg\) rotateY\(0(\.0+)?deg\)/)
  })

  it('marks the whole shell static under reduced motion, so loops can be switched off', () => {
    mockMedia({ 'prefers-reduced-motion': true, 'pointer: coarse': false })
    const { container } = render(<AuthShell title="t"><span>form</span></AuthShell>)
    expect(container.querySelector('[data-motion="static"]')).toBeTruthy()
    expect(container.querySelector('[data-motion="animated"]')).toBeNull()
  })

  it('re-keys the card when the step changes, so a new step animates in', () => {
    const { container, rerender } = render(
      <AuthShell title="t" stepKey="credentials"><span>form</span></AuthShell>,
    )
    const card = () => container.querySelector('[data-testid="auth-card"]') as HTMLElement
    expect(card().getAttribute('data-step')).toBe('credentials')
    rerender(<AuthShell title="t" stepKey="mfa-verify"><span>form</span></AuthShell>)
    expect(card().getAttribute('data-step')).toBe('mfa-verify')
  })
})

describe('AuthHero', () => {
  it('is decorative — hidden from assistive tech, with no accessible name', () => {
    const { container } = render(<AuthHero />)
    const svg = container.querySelector('svg')
    expect(svg).toBeTruthy()
    expect(svg?.getAttribute('aria-hidden')).toBe('true')
    expect(svg?.querySelector('title')).toBeNull()
  })

  it('renders its sealed end state when motion is off, not a blank stage', () => {
    const { container } = render(<AuthHero motion="static" />)
    // Every animated part must still be in the DOM and unanimated.
    expect(container.querySelectorAll('[data-part="page"]').length).toBeGreaterThan(0)
    expect(container.querySelector('[data-part="lock"]')).toBeTruthy()
    expect(container.querySelector('[data-motion="animated"]')).toBeNull()
  })
})
