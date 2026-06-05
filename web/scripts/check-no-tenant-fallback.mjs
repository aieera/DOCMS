#!/usr/bin/env node
// Audit H-1 regression guard. Run via `npm run lint:tenant` and in CI,
// alongside the mojibake (lint:utf8) and physical-Tailwind (lint:rtl)
// guards.
//
// The trap: `user.tenant_id ?? ''` (or any `tenant_id ?? ''`) defaults a
// missing tenant id to the EMPTY STRING. An empty tenant id then sails
// past truthiness-lite checks, gets stuffed into the auth store, and
// leaves X-Auth-Tenant-ID off every follow-up request — server-side
// identity checks fail open and the user sees a broken session. The
// orphan `useAuth.useLogin` hook carried exactly this fallback and was
// deleted in the 2026-05 audit (Wave 5 pattern 5); lib/finalizeLogin.ts
// is the one sanctioned path and rejects an empty-string tenant id.
//
// The safe fallback is `?? null` (or `?? undefined`) — a missing tenant
// id stays falsy/sentinel and the consuming code fails CLOSED. Those are
// NOT flagged; only the empty-string default is.
//
// Opt-out: prefix the offending line OR the line directly above it with
// the comment marker `tenant-fallback: doc-reference`. Use this ONLY for
// comments/tests that quote the bad pattern to document it (this script,
// the useAuth deletion note, the finalizeLogin regression test) — never
// to silence a real `?? ''` in live code.
//
// Exits non-zero if any unflagged occurrence is found in web/src/.

import { readFileSync } from 'node:fs'
import { readdir } from 'node:fs/promises'
import { join, dirname, resolve, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = resolve(__dirname, '..', '..')
const SRC_ROOT = resolve(__dirname, '..', 'src')

// The literal H-1 trap. Matches `tenant_id ?? ''` and `tenant_id ?? ""`
// (single- or double-quoted empty string), with flexible whitespace
// around the `??` so a reformat can't sneak it past.
const PATTERN = /tenant_id\s*\?\?\s*(''|"")/

const EXTS = new Set(['.ts', '.tsx', '.js', '.jsx', '.mjs', '.cjs'])

// Directories we never scan: build artefacts, generated files.
const SKIP_DIRS = new Set([
  'node_modules', 'dist', 'build', 'coverage', '.git',
  'generated', 'gen',
])

// Files to skip outright. This script documents the byte pattern in its
// banner; scanning it would trip on every run.
const SKIP_FILES = new Set([
  'check-no-tenant-fallback.mjs',
  'routeTree.gen.ts',
])

// Inline opt-out marker for comments/tests that quote the pattern to
// document it. Same shape as check-no-physical-tw's marker.
const MARKER = 'tenant-fallback: doc-reference'

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

function lineIsAllowlisted(lines, i) {
  // Allowlist if EITHER this line or the line directly above carries
  // the marker. The "line above" form reads like a comment justifying
  // the doc reference.
  return (
    lines[i].includes(MARKER) ||
    (i > 0 && lines[i - 1].includes(MARKER))
  )
}

async function main() {
  const hits = []
  for await (const path of walk(SRC_ROOT)) {
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
    const lines = content.split('\n')
    for (let i = 0; i < lines.length; i++) {
      if (!PATTERN.test(lines[i])) continue
      if (lineIsAllowlisted(lines, i)) continue
      hits.push({
        path: relative(REPO_ROOT, path),
        line: i + 1,
        text: lines[i].trim(),
      })
    }
  }

  if (hits.length === 0) {
    console.log("check-no-tenant-fallback: ✓ no `tenant_id ?? ''` H-1 traps found")
    return
  }

  for (const h of hits) {
    console.error(`${h.path}:${h.line}  ${h.text}`)
  }
  console.error(`\n✗ ${hits.length} \`tenant_id ?? ''\` H-1 trap(s) found.`)
  console.error(
    "Fix: default a missing tenant id to `null`/`undefined` (fail closed),\n" +
      'or route the login through lib/finalizeLogin.ts which rejects an empty\n' +
      'tenant id. A doc reference that quotes the pattern can opt out with the\n' +
      `comment marker: // ${MARKER}`,
  )
  process.exit(1)
}

main().catch((err) => {
  console.error('check-no-tenant-fallback: unexpected error:', err)
  process.exit(2)
})
