#!/usr/bin/env node
// migrate-logical-tw.mjs — ADR 0107 mechanical migration script.
//
// Walks every web/src/**/*.{ts,tsx,css} file and rewrites physical
// Tailwind utilities to their logical equivalents:
//
//   ml-N      → ms-N           pl-N      → ps-N
//   mr-N      → me-N           pr-N      → pe-N
//   -ml-N     → -ms-N          -pl-N     → -ps-N
//   -mr-N     → -me-N          -pr-N     → -pe-N
//   text-left → text-start     text-right → text-end
//   border-l  → border-s       border-r   → border-e
//   border-l-N → border-s-N    border-r-N → border-e-N
//   rounded-l-N → rounded-s-N  rounded-r-N → rounded-e-N
//   rounded-tl/tr/bl/br are LEFT ALONE — those are physical corners
//     and Tailwind has no logical equivalent (`rounded-ss/se/es/ee`
//     are 3.4+ but uncommon; manual review is safer).
//   left-N    → start-N        right-N   → end-N
//
// Word boundary handling:
//   * Class prefixes like `hover:`, `lg:`, `dark:` survive because
//     the regex anchors on a non-word char before the utility.
//   * JS identifiers like `leftIcon`, `rightArrow`, `props.left` are
//     NOT touched — they don't contain `left-` or `right-` followed
//     by a digit/keyword.
//   * Arbitrary values `ml-[14px]` and decimal `ml-1.5` are matched.
//
// What this script DELIBERATELY skips:
//   * `space-x-N` — requires per-call review (flex+gap is often the
//     better swap). The CI lint rule (Prompt 3 next step) flags new
//     occurrences.
//   * rounded-{tl,tr,bl,br} — see above.
//   * Anything in chart libraries (recharts, cytoscape) — those use
//     `textAnchor="start"`/`"end"` props or SVG transforms, not
//     className strings, so the regex shouldn't match. Verified by
//     spot-check after run.
//   * SVG path data + transforms — same reason.

import { readFileSync, writeFileSync, statSync } from 'node:fs'
import { readdir } from 'node:fs/promises'
import { join, extname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const __dirname = dirname(fileURLToPath(import.meta.url))
const ROOT = resolve(__dirname, '..', 'src')
const EXTS = new Set(['.ts', '.tsx', '.css'])

// Skip directories that pull in vendored / generated content. The
// generated route tree and the i18n bundle are regenerated on build;
// touching them is pure noise.
const SKIP_DIRS = new Set([
  'node_modules', 'dist', 'build', '.next', 'generated',
])
const SKIP_FILES = new Set([
  'routeTree.gen.ts',
])

// Match a Tailwind utility token. The boundary chars before the
// token MUST be one of: start-of-line, whitespace, quote, ` or one of
// the variant separators (`:` for `hover:`, `[` for arbitrary-variant,
// `(` for clsx() args). `-` is also legal as a leading char so the
// negative-margin form `-ml-4` matches (the `-` is part of the
// utility, not a word-boundary char).
//
// We capture the OPTIONAL negative `-` prefix in group 1 and the
// value tail in group 2.
//
// Value pattern: digits, letters, `.`, `/`, `[...]`. Stops at any
// whitespace, quote, backtick, `,`, `}`, `]`, or `)`. That covers
// `ml-4`, `ml-1.5`, `ml-1/2`, `ml-px`, `ml-auto`, `ml-[14px]`.
const VALUE_TAIL = '([\\w./]+|\\[[^\\]]+\\])'
const PREFIX     = '(^|[\\s"\'`:>(])(-?)'

function rewrite(src) {
  let out = src
  let changes = 0

  // Helper that applies one rule and tracks changes.
  function sub(re, replacer) {
    out = out.replace(re, (m, ...args) => {
      changes++
      return replacer(m, ...args)
    })
  }

  // --- margin -----------------------------------------------------
  // ml-N → ms-N, mr-N → me-N (with optional negative prefix).
  sub(
    new RegExp(`${PREFIX}ml-${VALUE_TAIL}\\b`, 'g'),
    (_m, lead, neg, val) => `${lead}${neg}ms-${val}`,
  )
  sub(
    new RegExp(`${PREFIX}mr-${VALUE_TAIL}\\b`, 'g'),
    (_m, lead, neg, val) => `${lead}${neg}me-${val}`,
  )

  // --- padding ----------------------------------------------------
  sub(
    new RegExp(`${PREFIX}pl-${VALUE_TAIL}\\b`, 'g'),
    (_m, lead, neg, val) => `${lead}${neg}ps-${val}`,
  )
  sub(
    new RegExp(`${PREFIX}pr-${VALUE_TAIL}\\b`, 'g'),
    (_m, lead, neg, val) => `${lead}${neg}pe-${val}`,
  )

  // --- position (left-N, right-N) ---------------------------------
  // ONLY match positional utilities followed by a numeric/keyword
  // tail. This keeps JS identifiers like `leftIcon`, `rightArrow`,
  // `text-left` (handled separately) intact.
  sub(
    new RegExp(`${PREFIX}left-${VALUE_TAIL}\\b`, 'g'),
    (_m, lead, neg, val) => `${lead}${neg}start-${val}`,
  )
  sub(
    new RegExp(`${PREFIX}right-${VALUE_TAIL}\\b`, 'g'),
    (_m, lead, neg, val) => `${lead}${neg}end-${val}`,
  )

  // --- text-align -------------------------------------------------
  sub(/\btext-left\b/g, () => 'text-start')
  sub(/\btext-right\b/g, () => 'text-end')

  // --- border-l / border-r ----------------------------------------
  // border-l-N → border-s-N (N optional). Use VALUE_TAIL so widths
  // like `border-l-2`, `border-l-[1px]`, AND the bare `border-l` all
  // resolve. The bare form has no `-N` so a separate rule handles it.
  sub(
    new RegExp(`${PREFIX}border-l-${VALUE_TAIL}\\b`, 'g'),
    (_m, lead, _neg, val) => `${lead}border-s-${val}`,
  )
  sub(
    new RegExp(`${PREFIX}border-r-${VALUE_TAIL}\\b`, 'g'),
    (_m, lead, _neg, val) => `${lead}border-e-${val}`,
  )
  sub(/\bborder-l\b(?!-)/g, () => 'border-s')
  sub(/\bborder-r\b(?!-)/g, () => 'border-e')

  // --- rounded-l / rounded-r --------------------------------------
  // rounded-l-N → rounded-s-N. We deliberately do NOT touch the
  // corner-specific forms rounded-{tl,tr,bl,br}-N — those are
  // physical corners and Tailwind's logical equivalents
  // (rounded-ss/se/es/ee) are too rarely used; manual review is
  // safer than a blind swap.
  sub(
    new RegExp(`${PREFIX}rounded-l-${VALUE_TAIL}\\b`, 'g'),
    (_m, lead, _neg, val) => `${lead}rounded-s-${val}`,
  )
  sub(
    new RegExp(`${PREFIX}rounded-r-${VALUE_TAIL}\\b`, 'g'),
    (_m, lead, _neg, val) => `${lead}rounded-e-${val}`,
  )
  sub(/\brounded-l\b(?!-)/g, () => 'rounded-s')
  sub(/\brounded-r\b(?!-)/g, () => 'rounded-e')

  return { out, changes }
}

async function walk(dir) {
  const ents = await readdir(dir, { withFileTypes: true })
  const files = []
  for (const e of ents) {
    if (SKIP_DIRS.has(e.name)) continue
    if (SKIP_FILES.has(e.name)) continue
    const p = join(dir, e.name)
    if (e.isDirectory()) {
      files.push(...(await walk(p)))
    } else if (e.isFile() && EXTS.has(extname(e.name))) {
      files.push(p)
    }
  }
  return files
}

async function main() {
  const files = await walk(ROOT)
  let touchedFiles = 0
  let totalChanges = 0
  for (const f of files) {
    const src = readFileSync(f, 'utf8')
    const { out, changes } = rewrite(src)
    if (changes > 0) {
      writeFileSync(f, out)
      touchedFiles++
      totalChanges += changes
      console.log(`  ${f.replace(ROOT, 'src')}  (+${changes})`)
    }
  }
  console.log('—')
  console.log(`  ${touchedFiles} file(s) rewritten, ${totalChanges} occurrence(s) migrated`)
  console.log('  Review the diff before committing — especially position utilities (left-/right-).')
  console.log('  rounded-{tl,tr,bl,br} were LEFT ALONE; review manually.')
  console.log('  space-x-N was LEFT ALONE; review manually.')
}

main()
