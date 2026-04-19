import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import axe from 'axe-core'

import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'

// axe-core finds WCAG 2.2 AA violations in a rendered DOM subtree. We run
// it against the handful of components we can render without a router /
// router-context. The tanstack-router file routes need a RouterProvider
// wrapper which is out of scope for a smoke test; adding that here would
// drag the whole app boot into the a11y pass.
async function expectNoViolations(element: HTMLElement) {
  const results = await axe.run(element, {
    runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'] },
  })
  // Surface every violation so the failure message points the dev at the
  // exact rule + node instead of a generic "didn't pass" line.
  if (results.violations.length > 0) {
    const summary = results.violations
      .map((v) => `[${v.id}] ${v.help} — ${v.nodes.length} node(s)`)
      .join('\n')
    throw new Error(`axe violations:\n${summary}`)
  }
  expect(results.violations).toEqual([])
}

describe('a11y — axe smoke', () => {
  it('PageHeader has no violations', async () => {
    const { container } = render(
      <PageHeader title="Users" description="Manage team members" />,
    )
    await expectNoViolations(container)
  })

  it('Button has no violations', async () => {
    const { container } = render(<Button>Save</Button>)
    await expectNoViolations(container)
  })

  it('Disabled Button has no violations', async () => {
    // Disabled state is its own rule family (color-contrast, focusable,
    // etc.) — pin it separately.
    const { container } = render(<Button disabled>Save</Button>)
    await expectNoViolations(container)
  })
})
