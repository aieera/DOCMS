// Pointer effects on the auth screens (2026-09-23). The risk here is not
// "does it look nice" — it is that a decorative layer running on every
// mouse move can eat clicks, re-render a form 60 times a second, keep a
// laptop awake, or ignore someone's reduced-motion setting. These pin the
// guarantees, not the visuals.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'

import { AuthShell } from '../auth-shell'
import { Magnetic } from '@/components/ui/Magnetic'

// The shell renders the brand mark as a router Link; without a router
// context it throws on `isServer`.
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, ...rest }: { children: React.ReactNode; to?: string } & Record<string, unknown>) =>
    <a href={to} {...rest}>{children}</a>,
}))

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

/** The x/y a transform actually resolves to, so assertions compare numbers
 *  rather than string formatting ("0px" vs "0.00px"). */
function offset(el: HTMLElement): [number, number] {
  const m = /translate3d\(([-\d.]+)px,\s*([-\d.]+)px/.exec(el.style.transform)
  return m ? [Number(m[1]), Number(m[2])] : [0, 0]
}

const RECT = {
  width: 800, height: 600, left: 0, top: 0, right: 800, bottom: 600, x: 0, y: 0,
  toJSON: () => ({}),
} as DOMRect

let rafCalls = 0

beforeEach(() => {
  mockMedia({ 'prefers-reduced-motion': false, 'pointer: coarse': false })
  rafCalls = 0
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => { rafCalls++; cb(16); return rafCalls })
  vi.stubGlobal('cancelAnimationFrame', vi.fn())
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue(RECT)
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function renderShell() {
  return render(<AuthShell title="Welcome back"><button type="button">Sign in</button></AuthShell>)
}

describe('PointerFx layer', () => {
  it('is inert and invisible to assistive tech, so it can never eat a click', () => {
    const { container } = renderShell()
    const layer = container.querySelector('[data-testid="pointer-fx"]') as HTMLElement
    expect(layer).toBeTruthy()
    expect(layer.getAttribute('aria-hidden')).toBe('true')
    // Inline style, not a class, so the guarantee survives a CSS purge.
    expect(layer.style.pointerEvents).toBe('none')
  })

  it('publishes the pointer position on the surface as CSS custom properties', () => {
    const { container } = renderShell()
    const root = container.querySelector('[data-motion]') as HTMLElement
    fireEvent.pointerMove(window, { clientX: 400, clientY: 300 })
    expect(root.style.getPropertyValue('--fx-x')).not.toBe('')
    expect(root.style.getPropertyValue('--fx-y')).not.toBe('')
  })

  it('publishes nothing under reduced motion', () => {
    mockMedia({ 'prefers-reduced-motion': true, 'pointer: coarse': false })
    const { container } = renderShell()
    const root = container.querySelector('[data-motion]') as HTMLElement
    fireEvent.pointerMove(window, { clientX: 400, clientY: 300 })
    expect(root.style.getPropertyValue('--fx-x')).toBe('')
  })

  it('renders no trailing orbs under reduced motion', () => {
    mockMedia({ 'prefers-reduced-motion': true, 'pointer: coarse': false })
    const { container } = renderShell()
    expect(container.querySelectorAll('[data-part="orb"]').length).toBe(0)
  })

  it('renders no trailing orbs on a touch device', () => {
    mockMedia({ 'prefers-reduced-motion': false, 'pointer: coarse': true })
    const { container } = renderShell()
    expect(container.querySelectorAll('[data-part="orb"]').length).toBe(0)
  })

  it('renders trailing orbs on a fine pointer', () => {
    const { container } = renderShell()
    expect(container.querySelectorAll('[data-part="orb"]').length).toBeGreaterThan(0)
  })

  it('never re-renders the form while the pointer moves', () => {
    let formRenders = 0
    function Probe() { formRenders++; return <button type="button">Sign in</button> }
    const { container } = render(<AuthShell title="t"><Probe /></AuthShell>)
    const after = formRenders
    for (let i = 0; i < 12; i++) {
      fireEvent.pointerMove(window, { clientX: 100 + i, clientY: 100 + i })
    }
    expect(container.querySelector('[data-testid="pointer-fx"]')).toBeTruthy()
    expect(formRenders).toBe(after)
  })

  it('ripples on click, and still does so on a touch device', () => {
    mockMedia({ 'prefers-reduced-motion': false, 'pointer: coarse': true })
    const { container } = renderShell()
    fireEvent.pointerDown(window, { clientX: 120, clientY: 90 })
    expect(container.querySelectorAll('[data-part="ripple"]').length).toBe(1)
  })

  it('does not ripple under reduced motion', () => {
    mockMedia({ 'prefers-reduced-motion': true, 'pointer: coarse': false })
    const { container } = renderShell()
    fireEvent.pointerDown(window, { clientX: 120, clientY: 90 })
    expect(container.querySelectorAll('[data-part="ripple"]').length).toBe(0)
  })
})

describe('Magnetic', () => {
  function renderMagnetic() {
    return render(<Magnetic><button type="button">Sign in</button></Magnetic>)
  }

  it('leans toward a nearby pointer', () => {
    const { container } = renderMagnetic()
    const el = container.firstElementChild as HTMLElement
    fireEvent.pointerMove(window, { clientX: 420, clientY: 310 })
    expect(offset(el).some((v) => v !== 0)).toBe(true)
  })

  it('stays put under reduced motion', () => {
    mockMedia({ 'prefers-reduced-motion': true, 'pointer: coarse': false })
    const { container } = renderMagnetic()
    const el = container.firstElementChild as HTMLElement
    fireEvent.pointerMove(window, { clientX: 420, clientY: 310 })
    expect(offset(el)).toEqual([0, 0])
  })

  it('stays put on a touch device', () => {
    mockMedia({ 'prefers-reduced-motion': false, 'pointer: coarse': true })
    const { container } = renderMagnetic()
    const el = container.firstElementChild as HTMLElement
    fireEvent.pointerMove(window, { clientX: 420, clientY: 310 })
    expect(offset(el)).toEqual([0, 0])
  })

  it('releases on pointerdown, so the button cannot slide out from under a click', () => {
    const { container } = renderMagnetic()
    const el = container.firstElementChild as HTMLElement
    fireEvent.pointerMove(window, { clientX: 420, clientY: 310 })
    expect(offset(el).some((v) => v !== 0)).toBe(true)
    fireEvent.pointerDown(window, { clientX: 420, clientY: 310 })
    expect(offset(el)).toEqual([0, 0])
  })

  it('keeps the child clickable', () => {
    const onClick = vi.fn()
    render(<Magnetic><button type="button" onClick={onClick}>Sign in</button></Magnetic>)
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(onClick).toHaveBeenCalledTimes(1)
  })
})
