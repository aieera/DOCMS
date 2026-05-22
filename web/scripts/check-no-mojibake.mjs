#!/usr/bin/env node
// Wave 5 root-cause guard for M-8: catch UTF-8 mojibake before it
// ships. The original bug was a file (main.tsx) saved as Latin-1 then
// re-decoded as UTF-8, producing "Loadingâ€¦" in place of "Loading…".
// .editorconfig pins `charset = utf-8` for new files in conforming
// editors, but legacy editors, OS clipboards, copy-paste from PDF
// previews, and AI tooling occasionally re-introduce the byte
// sequences this script flags.
//
// What we look for: the seven-byte and two-byte patterns produced
// by encoding a UTF-8-encoded smart-punct codepoint as a Latin-1
// string and then re-encoding that ASCII as UTF-8. The signature is
// the literal three bytes `0xC3 0xA2 0xE2 0x82 0xAC` (i.e. "â€")
// followed by a third byte that selects the original codepoint:
//   â€¦   U+2026 …  (HORIZONTAL ELLIPSIS)
//   â€™   U+2019 '  (RIGHT SINGLE QUOTATION MARK)
//   â€"   U+2014 —  (EM DASH)
//   â€"   U+2013 –  (EN DASH)
//   â€œ   U+201C "  (LEFT DOUBLE QUOTATION MARK)
//   â€    U+201D "  (RIGHT DOUBLE QUOTATION MARK; followed by U+009D)
//
// Treats the script file itself as the authoritative documentation
// of the byte sequences — and skips it during the scan so the
// examples-in-comments don't trigger the check.
//
// Exits non-zero if any byte sequence is found in scanned files.

import { readFileSync, statSync } from 'node:fs'
import { readdir } from 'node:fs/promises'
import { join, dirname, resolve, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = resolve(__dirname, '..', '..')
const WEB_ROOT = resolve(__dirname, '..')

// Mojibake byte sequences to flag. Each entry: [pattern, codepoint
// it ought to be, human-friendly name]. Sequence is the literal
// three-byte CP-1252-via-UTF-8 representation of "â€" plus a
// trailing byte that selects the original character.
const MOJIBAKE = [
  ['â€¦', '…',  'horizontal ellipsis (U+2026)'],
  ['â€™', "'",  'right single quote (U+2019)'],
  ['â€“', '–',  'en dash (U+2013)'],
  ['â€”', '—',  'em dash (U+2014)'],
  ['â€œ', '"',  'left double quote (U+201C)'],
  ['â€', '"',  'right double quote (U+201D)'],
]

const EXTS = new Set([
  '.ts', '.tsx', '.js', '.jsx', '.mjs', '.cjs',
  '.css', '.html', '.md', '.json', '.yaml', '.yml',
  '.go', '.py', '.sh', '.sql', '.proto',
])

// Directories we never scan: build artefacts, third-party code,
// generated files, lock files. Path components, not full paths.
const SKIP_DIRS = new Set([
  'node_modules', 'dist', 'build', 'coverage', '.git',
  'generated', 'gen', '.next',
  'test-results', 'playwright-report',
])

// Specific files to skip. The check script itself documents the byte
// sequences in its banner comment; scanning it would trip on every
// run.
const SKIP_FILES = new Set([
  'check-no-mojibake.mjs',
  'package-lock.json',
  'yarn.lock',
  'pnpm-lock.yaml',
  'go.sum',
  'go.work.sum',
])

async function* walk(dir) {
  let entries
  try {
    entries = await readdir(dir, { withFileTypes: true })
  } catch {
    return
  }
  for (const e of entries) {
    if (e.isDirectory()) {
      if (SKIP_DIRS.has(e.name)) continue
      yield* walk(join(dir, e.name))
    } else if (e.isFile()) {
      yield join(dir, e.name)
    }
  }
}

async function main() {
  // Scan web/src first (the original M-8 site), then the rest of
  // web/ excluding standard noise. Keeps the scope tight; backend
  // services would need their own runner if they ever ship UI copy.
  const roots = [WEB_ROOT]
  const hits = []
  for (const root of roots) {
    for await (const path of walk(root)) {
      const base = path.split(/[\\/]/).pop()
      if (SKIP_FILES.has(base)) continue
      const dot = base.lastIndexOf('.')
      const ext = dot >= 0 ? base.slice(dot) : ''
      if (!EXTS.has(ext)) continue
      let content
      try {
        content = readFileSync(path, 'utf8')
      } catch {
        continue
      }
      for (const [pattern, want, label] of MOJIBAKE) {
        let idx = content.indexOf(pattern)
        while (idx >= 0) {
          // Compute 1-based line number for nicer error output.
          const before = content.slice(0, idx)
          const line = before.split('\n').length
          hits.push({
            path: relative(REPO_ROOT, path),
            line,
            want,
            label,
            sample: content.slice(Math.max(0, idx - 12), idx + pattern.length + 12),
          })
          idx = content.indexOf(pattern, idx + 1)
        }
      }
    }
  }

  if (hits.length === 0) {
    console.log('check-no-mojibake: ✓ no mojibake byte sequences found')
    return
  }

  for (const h of hits) {
    console.error(
      `${h.path}:${h.line}  ${h.label} — expected ${JSON.stringify(h.want)}\n` +
        `  context: ${JSON.stringify(h.sample)}`,
    )
  }
  console.error(`\n✗ ${hits.length} mojibake byte sequence(s) found.`)
  console.error(
    'Fix: open each file in a UTF-8-aware editor, replace the byte sequence with the\n' +
      'codepoint shown above, and save as UTF-8 without BOM.',
  )
  process.exit(1)
}

main().catch((err) => {
  console.error('check-no-mojibake: unexpected error:', err)
  process.exit(2)
})
