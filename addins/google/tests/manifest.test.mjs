// W-8: appsscript.json hard-coded the example domain in
// openLinkUrlPrefixes / openLink / logoUrl, so every real deployment
// either edited the file by hand or shipped an add-on whose "Open in
// SeDoc" links were refused. The hosts are now ${TOKENS}, rendered into
// build/ by render-manifest.mjs (which fails on an unset token).
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { renderManifest } from '../render-manifest.mjs'

const src = readFileSync(resolve(import.meta.dirname, '../appsscript.json'), 'utf8')

describe('Apps Script manifest (W-8)', () => {
  it('has no hard-coded example host', () => {
    expect(src).not.toMatch(/vaultdms\.example|sedoc\.example/)
    const m = JSON.parse(src)
    expect(m.addOns.common.openLinkUrlPrefixes).toContain('https://${SEDOC_WEB_HOST}/')
    expect(m.addOns.common.universalActions[0].openLink).toBe('https://${SEDOC_WEB_HOST}/')
  })

  it('renders tokens from the environment', () => {
    const out = JSON.parse(renderManifest(src, { SEDOC_WEB_HOST: 'app.acme.com', SEDOC_ADDIN_HOST: 'addin.acme.com' }))
    expect(out.addOns.common.openLinkUrlPrefixes).toEqual(['https://app.acme.com/', 'https://docs.google.com/'])
    expect(out.addOns.common.logoUrl).toBe('https://addin.acme.com/google/logo.png')
    expect(JSON.stringify(out)).not.toContain('${')
  })

  it('refuses to render with a token unset', () => {
    expect(() => renderManifest(src, { SEDOC_ADDIN_HOST: 'addin.acme.com' })).toThrow(/SEDOC_WEB_HOST/)
  })

  it('rejects a host value that is not a bare hostname', () => {
    expect(() => renderManifest(src, { SEDOC_WEB_HOST: 'evil.com/x"', SEDOC_ADDIN_HOST: 'a.com' })).toThrow(/hostname/)
  })
})
