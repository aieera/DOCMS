import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useCountUp } from '../useCountUp'

function mockMatchMedia(reduced: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: reduced,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
}

describe('useCountUp', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

  it('returns the final value immediately when reduced motion is requested', () => {
    mockMatchMedia(true)
    const { result } = renderHook(() => useCountUp(120))
    expect(result.current).toBe(120)
  })

  it('starts below the target and lands exactly on it', () => {
    mockMatchMedia(false)
    let now = 0
    vi.spyOn(performance, 'now').mockImplementation(() => now)
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
      frames.push(cb); return frames.length
    })
    vi.stubGlobal('cancelAnimationFrame', vi.fn())

    const { result } = renderHook(() => useCountUp(100, 600))
    expect(result.current).toBe(0)

    act(() => { now = 300; frames.shift()?.(now) })
    expect(result.current).toBeGreaterThan(0)
    expect(result.current).toBeLessThan(100)

    act(() => { now = 600; frames.shift()?.(now) })
    expect(result.current).toBe(100)
  })

  // I5: the Unread tile shares the topbar's 30s poll; when 3 becomes 4
  // the tile used to flash 0, 1, 2, 3, 4 — a transient false 0.
  it('animates a changed value from the one on screen, never back through 0', () => {
    mockMatchMedia(false)
    let now = 0
    vi.spyOn(performance, 'now').mockImplementation(() => now)
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
      frames.push(cb); return frames.length
    })
    vi.stubGlobal('cancelAnimationFrame', vi.fn())

    const { result, rerender } = renderHook(({ v }) => useCountUp(v, 600), { initialProps: { v: 30 } })
    act(() => { now = 600; frames.shift()?.(now) })
    expect(result.current).toBe(30)

    frames.length = 0
    rerender({ v: 40 })
    expect(result.current).toBe(30)

    act(() => { now = 900; frames.shift()?.(now) })
    expect(result.current).toBeGreaterThan(30)
    expect(result.current).toBeLessThan(40)

    act(() => { now = 1200; frames.shift()?.(now) })
    expect(result.current).toBe(40)
  })

  it('does not animate a zero target', () => {
    mockMatchMedia(false)
    const { result } = renderHook(() => useCountUp(0))
    expect(result.current).toBe(0)
  })
})
