// Build-time configuration. RUNTIME override + BUILD-time DefinePlugin
// + a sane default; identical pattern to addins/outlook/src/config.ts.
const RUNTIME = (globalThis as unknown as { __SEDOC_API_BASE__?: string }).__SEDOC_API_BASE__
const BUILD   = (typeof process !== 'undefined' && process.env?.SEDOC_API_BASE) || ''

export const API_BASE = (RUNTIME || BUILD || 'https://app.vaultdms.example.com').replace(/\/+$/, '')
