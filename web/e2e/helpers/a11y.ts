import { createRequire } from 'node:module'
import type { Page, TestInfo } from '@playwright/test'
import { expect } from '@playwright/test'

// axe-core is already a runtime dep of the web app; we reuse the
// same copy from node_modules rather than pulling in the (wrapper-
// only) @axe-core/playwright package, which would be a new dep and
// require an ADR per Wave 15's no-new-deps rule.
//
// require.resolve hops through the ESM sandbox so the result is an
// absolute path to `axe-core/axe.min.js`, which addScriptTag can
// inject.
const localRequire = createRequire(import.meta.url)
const AXE_PATH = localRequire.resolve('axe-core/axe.min.js')

// AxeViolation matches the shape axe emits per rule hit.
interface AxeViolation {
  id: string
  impact: 'minor' | 'moderate' | 'serious' | 'critical' | null
  description: string
  help: string
  helpUrl: string
  nodes: { target: string[]; failureSummary?: string }[]
}

// BlockingImpacts are the levels that fail the suite. Per the Wave
// 15 brief (CC-4): "axe-core (no serious+critical)".
const BLOCKING: AxeViolation['impact'][] = ['serious', 'critical']

/**
 * expectAxeClean runs axe-core against the current page and fails
 * the test when any `serious` or `critical` violation is present.
 *
 * Attaches the violation list as a Playwright attachment so it's
 * easy to diff across runs even when the assertion passes.
 *
 * Keep the caller-provided `tag` short — it lands in the attachment
 * filename.
 */
export async function expectAxeClean(page: Page, testInfo: TestInfo, tag: string) {
  await page.addScriptTag({ path: AXE_PATH })

  const { violations } = (await page.evaluate(async () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const axe = (globalThis as any).axe
    return axe.run(document, {
      resultTypes: ['violations'],
      // runOnly tags match Lighthouse's a11y audit coverage — wcag2a,
      // wcag2aa, best-practice. Skipping `experimental` to avoid
      // false positives on early-stage rules.
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'best-practice'] },
    })
  })) as { violations: AxeViolation[] }

  const blocking = violations.filter((v) => BLOCKING.includes(v.impact))

  await testInfo.attach(`axe-${tag}.json`, {
    body: JSON.stringify(violations, null, 2),
    contentType: 'application/json',
  })

  if (blocking.length > 0) {
    const summary = blocking
      .map(
        (v) =>
          `  - ${v.impact?.toUpperCase()} ${v.id} (${v.nodes.length} node${v.nodes.length === 1 ? '' : 's'}): ${v.help}\n    ${v.helpUrl}`,
      )
      .join('\n')
    // eslint-disable-next-line no-console
    console.error(`axe violations (${tag}):\n${summary}`)
  }
  expect(
    blocking,
    `axe-core found ${blocking.length} serious/critical violation(s) on "${tag}"`,
  ).toEqual([])
}
