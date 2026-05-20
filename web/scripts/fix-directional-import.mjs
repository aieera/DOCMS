#!/usr/bin/env node
// One-shot fix for the import-placement bug in migrate-directional-icons.mjs.
// Removes the misplaced `import { DirectionalIcon } ...` line wherever it
// landed and re-inserts it AFTER the last full import statement.

import { readFileSync, writeFileSync } from 'node:fs'
import { readdir } from 'node:fs/promises'
import { join, extname, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const ROOT_SRC = resolve(__dirname, '..', 'src')

const NEEDLE = "import { DirectionalIcon } from '@/components/shared/DirectionalIcon'\n"

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

function fix(src) {
  if (!src.includes(NEEDLE)) return null
  // 1. Strip every occurrence (handles duplicates from prior re-runs).
  const stripped = src.split(NEEDLE).join('')

  // 2. Walk the file lexically tracking brace depth so we know when the
  //    final import statement ends. Imports can be multi-line:
  //      import {
  //        A, B, C,
  //      } from 'x'
  //    We need the offset right after the trailing `}` and the from clause's
  //    closing quote + newline.
  const lines = stripped.split('\n')
  let inImport = false
  let braceDepth = 0
  let lastImportEndLine = -1
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (!inImport) {
      if (/^import\b/.test(line)) {
        inImport = true
        braceDepth = (line.match(/\{/g) || []).length - (line.match(/\}/g) || []).length
        if (braceDepth === 0 && / from /.test(line)) {
          lastImportEndLine = i
          inImport = false
        }
      }
      continue
    }
    // Inside a multi-line import; track braces + look for the from clause.
    braceDepth += (line.match(/\{/g) || []).length - (line.match(/\}/g) || []).length
    if (braceDepth === 0 && / from /.test(line)) {
      lastImportEndLine = i
      inImport = false
    }
  }
  if (lastImportEndLine === -1) {
    // No imports found — drop it at the top.
    return NEEDLE + stripped
  }
  lines.splice(lastImportEndLine + 1, 0, NEEDLE.trimEnd())
  return lines.join('\n')
}

async function main() {
  const files = await walk(ROOT_SRC)
  let fixed = 0
  for (const f of files) {
    const src = readFileSync(f, 'utf8')
    const out = fix(src)
    if (out !== null && out !== src) {
      writeFileSync(f, out)
      fixed++
      console.log(`  ${f.replace(ROOT_SRC, 'src')}`)
    }
  }
  console.log(`— ${fixed} file(s) repaired`)
}

main()
