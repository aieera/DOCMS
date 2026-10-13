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

  // L-14: rAF timestamps come from the frame clock while `start` came
  // from performance.now(); a frame stamped earlier than start gave a
  // negative elapsed, and easeOut(negative) rendered a negative numeral
  // (the flaky KpiTile test).
  it('never renders outside [from, value] when a frame is stamped before start', () => {
    mockMatchMedia(false)
    vi.spyOn(performance, 'now').mockImplementation(() => 1000)
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
      frames.push(cb); return frames.length
    })
    vi.stubGlobal('cancelAnimationFrame', vi.fn())

    const { result } = renderHook(() => useCountUp(100, 600))
    act(() => { frames.shift()?.(200) }) // 800 ms "before" start
    expect(result.current).toBeGreaterThanOrEqual(0)
    expect(result.current).toBeLessThanOrEqual(100)
  })

  // L-14 (root cause): in a long-lived jsdom worker the rAF frame clock
  // lags performance.now() by seconds. Measuring elapsed from the frame
  // timestamp kept the count pinned at the start value until the test
  // timed out (the flaky KpiTile failure); it must finish on time.
  it('finishes on schedule even when the frame clock lags performance.now()', () => {
    mockMatchMedia(false)
    let now = 10_000
    vi.spyOn(performance, 'now').mockImplementation(() => now)
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
      frames.push(cb); return frames.length
    })
    vi.stubGlobal('cancelAnimationFrame', vi.fn())

    const { result } = renderHook(() => useCountUp(100, 600))
    const LAG = 8_000
    act(() => { now = 10_300; frames.shift()?.(now - LAG) })
    expect(result.current).toBeGreaterThan(0)
    act(() => { now = 10_600; frames.shift()?.(now - LAG) })
    expect(result.current).toBe(100)
  })
})
