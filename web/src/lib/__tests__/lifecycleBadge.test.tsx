// Why lifecycleVariant() has to exist, pinned as a test.
//
// The document service sends lifecycle_state as the raw proto enum
// (LIFECYCLE_STATE_DRAFT). Badge accepts any string, and cva applies NO
// variant classes for a value it does not recognise — `defaultVariants`
// only covers an absent prop, not an unknown one. So passing the wire
// value straight through renders a badge with no background and no
// colour: it degrades silently to plain text, and every lifecycle state
// looks identical.
import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'

import { Badge } from '@/components/ui/shadcn/badge'
import { lifecycleVariant } from '../lifecycle'

function classesOf(ui: React.ReactElement): string {
  const { container } = render(ui)
  return container.firstElementChild?.className ?? ''
}

describe('lifecycle badges', () => {
  it('renders an unstyled badge when the raw enum is passed straight through', () => {
    // This is the defect, asserted so nobody "simplifies" the helper away.
    const cls = classesOf(<Badge variant={'LIFECYCLE_STATE_ACTIVE' as string}>Active</Badge>)
    expect(cls).not.toMatch(/\bbg-(success|muted|warning|destructive|secondary|primary)/)
    expect(cls).not.toMatch(/\btext-(success|muted-foreground|warning-strong)/)
  })

  it('renders the lifecycle styling once the enum is normalised', () => {
    const cls = classesOf(<Badge variant={lifecycleVariant('LIFECYCLE_STATE_ACTIVE')}>Active</Badge>)
    expect(cls).toContain('text-success')
    expect(cls).toContain('bg-success/8')
  })

  it('keeps working for the short form the search index returns', () => {
    const cls = classesOf(<Badge variant={lifecycleVariant('draft')}>Draft</Badge>)
    expect(cls).toContain('bg-muted')
    expect(cls).toContain('text-muted-foreground')
  })

  it('falls back to a neutral badge for an unknown state', () => {
    const cls = classesOf(<Badge variant={lifecycleVariant('LIFECYCLE_STATE_UNSPECIFIED')}>—</Badge>)
    expect(cls).toContain('bg-secondary')
  })
})
