#!/usr/bin/env node
// Removes lucide-react named imports that no longer appear anywhere
// in the file. Targeted cleanup after the directional-icon migration.
//
// Heuristic: for each lucide-react import declaration, parse the
// named identifiers, then for each one check whether the file
// contains another occurrence after the import block. If not,
// drop the identifier. If the whole list becomes empty, drop the
// import statement entirely.
//
// Safe-by-default:
//   * Single-line imports only (multi-line imports are noisier to
//     parse safely; we'll fix any remaining cases by hand).
//   * Identifiers with aliases (`Foo as Bar`) keep the alias as the
//     name to check inside the file body.

import { readFileSync, writeFileSync } from 'node:fs'
import { readdir } from 'node:fs/promises'
import { join, extname, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const ROOT_SRC = resolve(__dirname, '..', 'src')

const LUCIDE_IMPORT_RE = /^import\s*\{\s*([^}]+?)\s*\}\s*from\s*['"]lucide-react['"]\s*;?\s*$/m

async function walk(dir) {
  const out = []
  for (const e of await readdir(dir, { withFileTypes: true })) {
    if (e.name === 'node_modules' || e.name === 'generated') continue
    const p = join(dir, e.name)
    if (e.isDirectory()) out.push(...(await walk(p)))
    else if (e.isFile() && extname(e.name) === '.tsx') out.push(p)
  }
  return out
}

function processFile(path) {
  const src = readFileSync(path, 'utf8')
  const m = src.match(LUCIDE_IMPORT_RE)
  if (!m) return null

  const list = m[1]
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)

  const importStart = m.index
  const importEnd = importStart + m[0].length
  const body = src.slice(importEnd)

  const survivors = []
  let stripped = 0
  for (const raw of list) {
    // Handle `Foo as Bar` aliasing — the file uses `Bar`.
    const aliasMatch = raw.match(/^(\w+)\s+as\s+(\w+)$/)
    const localName = aliasMatch ? aliasMatch[2] : raw
    // Word-boundary regex so `Chevron` doesn't match `ChevronUp`.
    const used = new RegExp(`\\b${localName}\\b`).test(body)
    if (used) survivors.push(raw)
    else stripped++
  }

  if (stripped === 0) return null

  let newImport
  if (survivors.length === 0) {
    // Drop the whole line.
    newImport = ''
  } else {
    newImport = `import { ${survivors.join(', ')} } from 'lucide-react'`
  }
  const before = src.slice(0, importStart)
  const after = src.slice(importEnd)
  // Strip trailing newline if dropping the line entirely so we don't
  // leave a blank.
  const sep = newImport === '' && after.startsWith('\n') ? after.slice(1) : after
  const out = before + newImport + (newImport ? after : sep)
  return { src: out, stripped, survivors: survivors.length }
}

async function main() {
  const files = await walk(ROOT_SRC)
  let fixed = 0
  let totalStripped = 0
  for (const f of files) {
    const res = processFile(f)
    if (!res) continue
    writeFileSync(f, res.src)
    fixed++
    totalStripped += res.stripped
    console.log(`  ${f.replace(ROOT_SRC, 'src')}  -${res.stripped}  (kept ${res.survivors})`)
  }
  console.log(`— ${fixed} file(s) cleaned, ${totalStripped} unused identifier(s) removed`)
}

main()
