import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { TONE } from '@/components/ui/crextio/calendar-week'
import { MetricBar } from '@/components/ui/crextio/metric-bar'
import { SegmentedProgress } from '@/components/ui/crextio/segmented-progress'

// Fix round 1 (task-7 review) — two dark-mode theme-correctness bugs:
//  1. StackedAvatar's TONE map had two byte-identical entries (b/d),
//     collapsing 5 tones to 4 indistinguishable circles.
//  2. The hatch-pattern "empty" texture on MetricBar/SegmentedProgress
//     used a hardcoded near-black SVG stroke (#1A1A1A), which has the
//     same luminance as dark mode's --muted track and disappears.

describe('StackedAvatar TONE — dark-mode-safe tone distinctness', () => {
  it('all 5 tones are mutually distinct token-driven treatments', () => {
    const values = Object.values(TONE)
    expect(values).toHaveLength(5)
    expect(new Set(values).size).toBe(5)
  })

  it('no tone hardcodes a warm hex color', () => {
    for (const v of Object.values(TONE)) {
      expect(v).not.toMatch(/#[0-9A-Fa-f]{3,6}/)
    }
  })
})

describe('MetricBar hatch texture — theme-reactive, not a fixed dark stroke', () => {
  it('the empty-fill hatch uses --muted-foreground, not a hardcoded near-black stroke', () => {
    const { container } = render(<MetricBar label="Interviews" value={0} />)
    const hatch = container.querySelector('[aria-hidden]') as HTMLElement
    expect(hatch).toBeTruthy()
    const bg = hatch.style.backgroundImage
    expect(bg).not.toMatch(/1A1A1A/i)
    expect(bg).toContain('--muted-foreground')
  })
})

describe('SegmentedProgress hatch texture — theme-reactive, not a fixed dark stroke', () => {
  it('the empty-segment hatch uses --muted-foreground, not a hardcoded near-black stroke', () => {
    const { container } = render(
      <SegmentedProgress segments={[{ label: '0%', variant: 'empty' }]} />,
    )
    const hatch = container.querySelector('[aria-hidden]') as HTMLElement
    expect(hatch).toBeTruthy()
    const bg = hatch.style.backgroundImage
    expect(bg).not.toMatch(/1A1A1A/i)
    expect(bg).toContain('--muted-foreground')
  })
})
