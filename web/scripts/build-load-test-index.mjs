#!/usr/bin/env node
// build-load-test-index.mjs — codegen for /admin/platform/load-tests.
//
// Reads every docs/load-tests/<YYYY-MM-DD>/summary.md, parses the
// YAML front-matter, and emits a typed TS module the admin page
// imports. The repo IS the report database — no backend endpoint
// is involved.
//
// Output: web/src/generated/load-test-reports.ts
//
// Run automatically via `npm run dev` and `npm run build` (predev /
// prebuild). Re-run after every new campaign summary lands in the
// repo so the FE picks it up at build time.

import { readFileSync, readdirSync, writeFileSync, mkdirSync, existsSync, statSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = resolve(__dirname, '..', '..')
const REPORTS_DIR = join(REPO_ROOT, 'docs', 'load-tests')
const OUT_FILE = join(REPO_ROOT, 'web', 'src', 'generated', 'load-test-reports.ts')

// Minimal YAML-front-matter parser: we only ship simple `key: "value"`
// scalars in the template so this avoids a yaml dep. If we ever need
// nested structures here we should pull in `js-yaml` instead.
function parseFrontmatter(md) {
  if (!md.startsWith('---')) return { meta: {}, body: md }
  const end = md.indexOf('\n---', 3)
  if (end === -1) return { meta: {}, body: md }
  const header = md.slice(3, end).trim()
  const body = md.slice(end + 4).replace(/^\n+/, '')
  const meta = {}
  for (const line of header.split('\n')) {
    const m = /^([\w-]+):\s*(.+?)\s*$/.exec(line)
    if (!m) continue
    let value = m[2]
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1)
    }
    meta[m[1]] = value
  }
  return { meta, body }
}

function listCampaigns() {
  if (!existsSync(REPORTS_DIR)) return []
  return readdirSync(REPORTS_DIR)
    .filter((name) => {
      if (name.startsWith('_') || name.startsWith('.')) return false
      const p = join(REPORTS_DIR, name)
      if (!statSync(p).isDirectory()) return false
      return existsSync(join(p, 'summary.md'))
    })
    .sort((a, b) => b.localeCompare(a))   // reverse chronological
}

function main() {
  const campaigns = listCampaigns()
  const records = campaigns.map((slug) => {
    const path = join(REPORTS_DIR, slug, 'summary.md')
    const raw = readFileSync(path, 'utf8')
    const { meta, body } = parseFrontmatter(raw)
    return {
      slug,
      meta: {
        date:        meta.date        ?? slug,
        campaign:    meta.campaign    ?? '',
        sut_version: meta.sut_version ?? '',
        helm_chart:  meta.helm_chart  ?? '',
        run_by:      meta.run_by      ?? '',
        verdict:     meta.verdict     ?? 'pending',
      },
      body,
    }
  })

  mkdirSync(dirname(OUT_FILE), { recursive: true })

  const ts = [
    '// AUTO-GENERATED — do not edit by hand.',
    '// Source: docs/load-tests/<date>/summary.md',
    '// Regenerate via: node scripts/build-load-test-index.mjs',
    '//',
    `// Generated at ${new Date().toISOString()} from ${campaigns.length} campaign(s).`,
    '',
    "export type Verdict = 'pending' | 'passed' | 'failed'",
    '',
    'export interface LoadTestReport {',
    '  slug: string',
    '  meta: {',
    '    date:        string',
    '    campaign:    string',
    '    sut_version: string',
    '    helm_chart:  string',
    '    run_by:      string',
    '    verdict:     Verdict',
    '  }',
    '  body: string',
    '}',
    '',
    `export const REPORTS: LoadTestReport[] = ${JSON.stringify(records, null, 2)} as const`,
    '',
  ].join('\n')

  writeFileSync(OUT_FILE, ts)
  console.log(`wrote ${OUT_FILE} (${campaigns.length} campaign${campaigns.length === 1 ? '' : 's'})`)
}

main()
