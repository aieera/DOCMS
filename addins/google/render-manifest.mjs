#!/usr/bin/env node
// W-8: Apps Script has no build step — `clasp push` uploads files
// verbatim — so the per-deployment hosts in appsscript.json are tokens
// rendered here. Usage:
//
//   SEDOC_WEB_HOST=app.acme.example.com \
//   SEDOC_ADDIN_HOST=addin.acme.example.com \
//   node render-manifest.mjs            # writes build/ (manifest + .gs)
//   cd build && clasp push              # .clasp.json lives in build/
//
// Tokens: ${SEDOC_WEB_HOST} (the SeDoc web app host — openLink targets
// and openLinkUrlPrefixes) and ${SEDOC_ADDIN_HOST} (static host serving
// google/logo.png). An unset token, or a value that is not a bare
// hostname[:port], fails the render.
import { readFileSync, writeFileSync, mkdirSync, readdirSync, copyFileSync, existsSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const HOST_RE = /^[a-z0-9.-]+(:\d{1,5})?$/i

export function renderManifest(text, env = process.env) {
  return text.replace(/\$\{([A-Z0-9_]+)\}/g, (_, name) => {
    const v = env[name]
    if (!v) throw new Error(`appsscript.json: ${name} is unset — export it before rendering`)
    if (!HOST_RE.test(v)) throw new Error(`appsscript.json: ${name} must be a bare hostname[:port], got ${JSON.stringify(v)}`)
    return v
  })
}

function main() {
  const here = dirname(fileURLToPath(import.meta.url))
  const out = join(here, 'build')
  mkdirSync(out, { recursive: true })
  const manifest = renderManifest(readFileSync(join(here, 'appsscript.json'), 'utf8'))
  JSON.parse(manifest) // must stay valid JSON
  writeFileSync(join(out, 'appsscript.json'), manifest)
  for (const f of readdirSync(here)) {
    if (f.endsWith('.gs')) copyFileSync(join(here, f), join(out, f))
  }
  if (existsSync(join(here, '.clasp.json'))) copyFileSync(join(here, '.clasp.json'), join(out, '.clasp.json'))
  console.log(`rendered ${out}`)
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) main()
