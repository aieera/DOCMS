#!/usr/bin/env node
// migrate-directional-icons.mjs — ADR 0108.
//
// Walks src/components/ + src/routes/ and replaces JSX usages of
// horizontal directional lucide icons with <DirectionalIcon> so
// they auto-mirror under dir="rtl".
//
// What this DOES touch:
//   * <ChevronLeft />, <ChevronLeft className="..." />, etc.
//   * <ChevronRight ... />, <ArrowLeft ... />, <ArrowRight ... />,
//     <CornerDownLeft ... />, <CornerDownRight ... />,
//     <ChevronsLeft ... />, <ChevronsRight ... />
//
// What this does NOT touch:
//   * Value-style usage like `icon: ChevronLeft` — those are
//     library hooks (shadcn primitives, react-day-picker IconLeft
//     prop). Their owners decide their own RTL behaviour.
//   * src/components/ui/shadcn/* — shadcn primitives are vendored
//     library code; we don't second-guess them.
//   * Vertical icons (ChevronUp/Down, ArrowUp/Down) — not
//     direction-dependent.
//   * Import statements — we add the DirectionalIcon import but
//     deliberately leave the original lucide imports in place. If
//     the file no longer uses the original icon as a value, tsc's
//     noUnusedLocals catches it and you remove by hand. That's
//     intentional: an auto-removed import on a file that still
//     uses the icon as a non-JSX value would break the build
//     silently.

import { readFileSync, writeFileSync } from 'node:fs'
import { readdir } from 'node:fs/promises'
import { join, extname, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const ROOT_SRC = resolve(__dirname, '..', 'src')
const SCAN_ROOTS = ['components', 'routes'].map((d) => join(ROOT_SRC, d))

// Allow-list of icons this migration owns. Stays in sync with
// the ICONS map in src/components/shared/DirectionalIcon.tsx.
const ICONS = [
  'ChevronLeft', 'ChevronRight',
  'ChevronsLeft', 'ChevronsRight',
  'ArrowLeft', 'ArrowRight',
  'ArrowLeftFromLine', 'ArrowRightFromLine',
  'ArrowLeftToLine', 'ArrowRightToLine',
  'CornerDownLeft', 'CornerDownRight',
  'CornerUpLeft', 'CornerUpRight',
  'MoveLeft', 'MoveRight',
]
const ICON_RE = new RegExp(`<(${ICONS.join('|')})(\\s|/|>)`, 'g')

const SKIP_PATH_PARTS = new Set(['shadcn', 'node_modules', 'generated'])

async function walk(dir) {
  const out = []
  for (const e of await readdir(dir, { withFileTypes: true })) {
    if (SKIP_PATH_PARTS.has(e.name)) continue
    const p = join(dir, e.name)
    if (e.isDirectory()) out.push(...(await walk(p)))
    else if (e.isFile() && extname(e.name) === '.tsx') out.push(p)
  }
  return out
}

function ensureImport(src) {
  // Idempotent: don't insert if the import is already there.
  if (/from ['"]@\/components\/shared\/DirectionalIcon['"]/.test(src)) {
    return src
  }
  // Place the new import after the last existing import. Falls
  // back to the file head if no imports exist (e.g. a CSS-side
  // file, which our walk skips anyway).
  const re = /^(import .+?\n)+/m
  const m = src.match(re)
  const importLine =
    "import { DirectionalIcon } from '@/components/shared/DirectionalIcon'\n"
  if (m) {
    const end = m.index + m[0].length
    return src.slice(0, end) + importLine + src.slice(end)
  }
  return importLine + src
}

function migrateFile(path) {
  let src = readFileSync(path, 'utf8')
  let touched = 0
  const out = src.replace(ICON_RE, (_match, name, follow) => {
    touched++
    // Preserve the character that followed the tag name — space
    // (props coming), `/` (self-closing no props), or `>` (no props
    // but with children).
    return `<DirectionalIcon name="${name}"${follow === '>' ? ' />' : follow}`
  })
  // The self-closing-no-props case: the original was `<ChevronLeft>`.
  // Our replacement above returned `<DirectionalIcon name="..." />`
  // (closing the tag) for that case, so the original `>` becomes
  // stray. Strip the orphaned `</ChevronLeft>` etc.
  let cleaned = out
  for (const name of ICONS) {
    cleaned = cleaned.replace(new RegExp(`</${name}>`, 'g'), '')
  }
  if (touched === 0) return null
  return { src: ensureImport(cleaned), touched }
}

async function main() {
  const files = (await Promise.all(SCAN_ROOTS.map(walk))).flat()
  let touchedFiles = 0
  let totalChanges = 0
  for (const f of files) {
    const res = migrateFile(f)
    if (!res) continue
    writeFileSync(f, res.src)
    touchedFiles++
    totalChanges += res.touched
    console.log(`  ${f.replace(ROOT_SRC, 'src')}  (+${res.touched})`)
  }
  console.log('—')
  console.log(`  ${touchedFiles} file(s), ${totalChanges} icon usage(s) migrated`)
  console.log('  Next: run `npx tsc --noEmit` and remove any now-unused lucide imports.')
}

main()
