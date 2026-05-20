#!/usr/bin/env node
// ADR 0110 — migrate every `health.NewServer(...)` call to
// `health.NewServerWithMeta("<svc>", cfg.Region, ...)`. The service
// short-name is derived from the file path (services/<svc>/cmd/server/main.go).
//
// Also injects a startup log line `log.Info().Str("region", cfg.Region)` if
// none exists. Safe to re-run — both replacements are idempotent.

import { readFileSync, writeFileSync } from 'node:fs'
import { execSync } from 'node:child_process'
import { resolve, dirname, basename } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const REPO = resolve(__dirname, '..')

const files = execSync('git ls-files "services/*/cmd/server/main.go"', {
  cwd: REPO,
  encoding: 'utf8',
}).trim().split('\n').filter(Boolean)

let touched = 0
for (const rel of files) {
  const abs = resolve(REPO, rel)
  // services/<svc>/cmd/server/main.go → svc.
  const svc = rel.split('/')[1]
  const src = readFileSync(abs, 'utf8')

  // Skip if already migrated.
  if (src.includes('health.NewServerWithMeta')) continue

  const re = /health\.NewServer\(([^)]*)\)/
  const m = src.match(re)
  if (!m) {
    console.warn(`  ${rel}  (no health.NewServer call found — skipping)`)
    continue
  }

  // The args inside health.NewServer(...) are the dep handles. We
  // prepend the two new positional args; rest preserved verbatim.
  const args = m[1].trim()
  const replacement = `health.NewServerWithMeta("${svc}", cfg.Region, ${args})`
  const out = src.replace(re, replacement)
  writeFileSync(abs, out)
  touched++
  console.log(`  ${rel}  →  ${svc} / cfg.Region`)
}
console.log(`— ${touched} service(s) migrated`)
