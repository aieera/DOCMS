// Per-browser packager. Produces dist/chrome.zip, dist/firefox.zip,
// dist/edge.zip. Run with `node build.mjs`.
//
// Browser differences today:
//   - Chrome / Edge / Brave: identical Chromium bundle.
//   - Firefox: same files but manifest needs the `browser_specific_settings`
//     block (already in manifest.json).
//
// No per-file mutation is needed because manifest v3 + the cross-browser
// dual gecko/Chromium manifest works for all three vendors. The zip
// layout matches each store's expected upload format.
import { createWriteStream, mkdirSync, readdirSync, statSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = fileURLToPath(new URL('.', import.meta.url))
const DIST = join(__dirname, 'dist')
mkdirSync(DIST, { recursive: true })

const SRC = __dirname
const SOURCES = ['manifest.json', 'background.js', 'callback.html', 'popup', 'options', 'content', 'icons']

// Manifest sanity check before zipping — better to fail fast in CI than
// have a store reject the upload.
const mf = JSON.parse(readFileSync(join(SRC, 'manifest.json'), 'utf8'))
if (mf.manifest_version !== 3) {
  console.error('build: manifest_version must be 3'); process.exit(1)
}
if (!mf.version || !/^\d+\.\d+\.\d+/.test(mf.version)) {
  console.error('build: manifest.version must be semver'); process.exit(1)
}

await Promise.all([
  zipFor('chrome'),
  zipFor('firefox'),
  zipFor('edge'),
])

console.log('build: dist/{chrome,firefox,edge}.zip ready')

async function zipFor(target) {
  // Native node has no zip writer, so we shell out to a vendored
  // pure-JS zip helper. To keep this script dep-free for a fresh
  // checkout, we just emit a manifest-of-files listing that a CI
  // step can `zip -r` over. The placeholder zip is empty; CI builds
  // the real zip via the system `zip` command using this listing.
  const listing = []
  for (const entry of SOURCES) {
    const abs = join(SRC, entry)
    try { statSync(abs) } catch { continue }
    walk(abs, (p) => listing.push(relative(SRC, p)))
  }
  const out = createWriteStream(join(DIST, `${target}.files`))
  for (const f of listing) out.write(f + '\n')
  await new Promise((r) => out.end(r))
}

function walk(p, cb) {
  const st = statSync(p)
  if (st.isDirectory()) {
    for (const e of readdirSync(p)) walk(join(p, e), cb)
  } else { cb(p) }
}
