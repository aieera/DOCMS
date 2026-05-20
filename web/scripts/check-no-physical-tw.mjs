#!/usr/bin/env node
// ADR 0107 — regression guard for the physical→logical Tailwind
// migration. Run via `npm run lint:rtl` and in CI.
//
// Flags any line that introduces a physical-direction Tailwind utility
// (ml-N, mr-N, pl-N, pr-N, left-N, right-N, text-left, text-right,
// border-l, border-r, rounded-l, rounded-r).
//
// Opt-out: prefix the offending line OR the line directly above it with
// the comment `// physical-direction: intentional`. Use this only for
// genuinely physical cases — e.g. SVG arrow icons, chart axes, code
// editors, video timelines. The intent of the marker is to make the
// reviewer slow down and ask "is this really physical?", not to
// silence the rule.
//
// Exits non-zero if any unflagged violation is found.

import { readFileSync } from 'node:fs'
import { readdir } from 'node:fs/promises'
import { join, extname, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const ROOT = resolve(__dirname, '..', 'src')
const EXTS = new Set(['.ts', '.tsx', '.css'])
const SKIP_DIRS = new Set(['node_modules', 'dist', 'build', 'generated'])
const SKIP_FILES = new Set(['routeTree.gen.ts'])

// Same shape as the migration script. Stays in sync with that source
// of truth: a match here is a missed swap there.
const VALUE_TAIL = '([\\w./]+|\\[[^\\]]+\\])'
const PREFIX     = '(^|[\\s"\'`:>(])(-?)'
const PATTERNS = [
  // margins
  new RegExp(`${PREFIX}ml-${VALUE_TAIL}\\b`),
  new RegExp(`${PREFIX}mr-${VALUE_TAIL}\\b`),
  // padding
  new RegExp(`${PREFIX}pl-${VALUE_TAIL}\\b`),
  new RegExp(`${PREFIX}pr-${VALUE_TAIL}\\b`),
  // position
  new RegExp(`${PREFIX}left-${VALUE_TAIL}\\b`),
  new RegExp(`${PREFIX}right-${VALUE_TAIL}\\b`),
  // text-align — these have no value-tail variant.
  /\btext-left\b/,
  /\btext-right\b/,
  // border-l / border-r — bare or value-tailed.
  new RegExp(`${PREFIX}border-l-${VALUE_TAIL}\\b`),
  new RegExp(`${PREFIX}border-r-${VALUE_TAIL}\\b`),
  /\bborder-l\b(?!-)/,
  /\bborder-r\b(?!-)/,
  // rounded-l / rounded-r
  new RegExp(`${PREFIX}rounded-l-${VALUE_TAIL}\\b`),
  new RegExp(`${PREFIX}rounded-r-${VALUE_TAIL}\\b`),
  /\brounded-l\b(?!-)/,
  /\brounded-r\b(?!-)/,
]

const MARKER = 'physical-direction: intentional'

async function walk(dir) {
  const ents = await readdir(dir, { withFileTypes: true })
  const out = []
  for (const e of ents) {
    if (SKIP_DIRS.has(e.name)) continue
    if (SKIP_FILES.has(e.name)) continue
    const p = join(dir, e.name)
    if (e.isDirectory()) out.push(...(await walk(p)))
    else if (e.isFile() && EXTS.has(extname(e.name))) out.push(p)
  }
  return out
}

function lineIsAllowlisted(lines, i) {
  // Allowlist if EITHER this line or the line directly above
  // contains the marker. The "line above" form is the canonical
  // style — it reads like a comment justifying the swap.
  return (
    lines[i].includes(MARKER) ||
    (i > 0 && lines[i - 1].includes(MARKER))
  )
}

async function main() {
  const files = await walk(ROOT)
  const violations = []
  for (const f of files) {
    const src = readFileSync(f, 'utf8')
    const lines = src.split('\n')
    for (let i = 0; i < lines.length; i++) {
      for (const re of PATTERNS) {
        if (!re.test(lines[i])) continue
        if (lineIsAllowlisted(lines, i)) break
        violations.push({ file: f.replace(ROOT, 'src'), line: i + 1, text: lines[i].trim() })
        break
      }
    }
  }
  if (violations.length === 0) {
    console.log('check-no-physical-tw: ✓ no regressions')
    return
  }
  console.error(`check-no-physical-tw: ${violations.length} violation(s) — ADR 0107 requires logical utilities`)
  for (const v of violations) {
    console.error(`  ${v.file}:${v.line}  ${v.text}`)
  }
  console.error('\nFix: replace with the logical equivalent')
  console.error('  ml-/mr-      → ms-/me-')
  console.error('  pl-/pr-      → ps-/pe-')
  console.error('  left-/right- → start-/end-')
  console.error('  text-left/right → text-start/end')
  console.error('  border-l/r   → border-s/e')
  console.error('  rounded-l/r  → rounded-s/e')
  console.error('Or annotate the line above with: // physical-direction: intentional')
  process.exit(1)
}

main()
