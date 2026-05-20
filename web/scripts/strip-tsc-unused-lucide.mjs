#!/usr/bin/env node
// Parses `tsc --noEmit` output and removes each reported unused
// identifier from its file's lucide-react import. Safer than the
// general strip script because it only touches identifiers tsc
// flagged.

import { execSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(__dirname, '..')

// 1. Capture tsc errors. Ignore the exit code — non-zero is expected
//    when there are errors to fix.
let tscOut = ''
try {
  execSync('npx tsc --noEmit', { cwd: WEB, encoding: 'utf8', stdio: 'pipe' })
} catch (e) {
  tscOut = (e.stdout || '') + (e.stderr || '')
}

// 2. Parse "file(line,col): error TS6133: 'Name' is declared but its value is never read."
const re6133 = /^(.+?)\((\d+),\d+\): error TS6133: '(\w+)' is declared/gm
const re6192 = /^(.+?)\(\d+,\d+\): error TS6192: All imports in import declaration are unused/gm

const targets = new Map() // file -> Set(identifier)
for (const m of tscOut.matchAll(re6133)) {
  const [, file, , name] = m
  if (!targets.has(file)) targets.set(file, new Set())
  targets.get(file).add(name)
}
// Files with "all imports unused" — we'll delete the lucide import line entirely.
const fullDrop = new Set()
for (const m of tscOut.matchAll(re6192)) {
  fullDrop.add(m[1])
}

if (targets.size === 0 && fullDrop.size === 0) {
  console.log('Nothing to strip.')
  process.exit(0)
}

const LUCIDE_LINE = /^import\s*\{\s*([^}]+?)\s*\}\s*from\s*['"]lucide-react['"]\s*;?\s*$/m

let touched = 0
for (const file of new Set([...targets.keys(), ...fullDrop])) {
  const abs = resolve(WEB, file)
  const src = readFileSync(abs, 'utf8')
  const m = src.match(LUCIDE_LINE)
  if (!m) continue

  if (fullDrop.has(file)) {
    // Whole line + trailing newline if any.
    const start = m.index
    const end = start + m[0].length
    const after = src.slice(end).startsWith('\n') ? end + 1 : end
    writeFileSync(abs, src.slice(0, start) + src.slice(after))
    touched++
    console.log(`  ${file}  (dropped whole import)`)
    continue
  }

  const drop = targets.get(file)
  const items = m[1].split(',').map((s) => s.trim()).filter(Boolean)
  const survivors = items.filter((raw) => {
    // Each raw element might be `Name`, `Name as Local`, or `type Name`.
    const local =
      raw.match(/^(?:type\s+)?(?:\w+\s+as\s+(\w+)|(\w+))$/)?.[1] ??
      raw.match(/^(?:type\s+)?(\w+)$/)?.[1] ??
      raw
    return !drop.has(local)
  })

  const newImport =
    survivors.length === 0
      ? ''
      : `import { ${survivors.join(', ')} } from 'lucide-react'`
  const start = m.index
  const end = start + m[0].length
  let nextStart = end
  if (newImport === '' && src[end] === '\n') nextStart = end + 1
  writeFileSync(abs, src.slice(0, start) + newImport + src.slice(nextStart))
  touched++
  console.log(`  ${file}  -${items.length - survivors.length}`)
}

console.log(`— ${touched} file(s) touched`)
