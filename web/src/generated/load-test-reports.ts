// AUTO-GENERATED — do not edit by hand.
// Source: docs/load-tests/<date>/summary.md
// Regenerate via: node scripts/build-load-test-index.mjs
//
// Generated at 2026-05-26T07:46:05.518Z from 0 campaign(s).

export type Verdict = 'pending' | 'passed' | 'failed'

export interface LoadTestReport {
  slug: string
  meta: {
    date:        string
    campaign:    string
    sut_version: string
    helm_chart:  string
    run_by:      string
    verdict:     Verdict
  }
  body: string
}

export const REPORTS: LoadTestReport[] = [] as const
