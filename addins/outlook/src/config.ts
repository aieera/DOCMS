// Build-time configuration for the Outlook add-in.
//
// The default points at the production hostname; webpack's
// DefinePlugin can override `SEDOC_API_BASE` at build time for
// staging / dev. Runtime override via window.__SEDOC_API_BASE__
// lets sideloaded copies talk to a custom backend without re-bundling
// the add-in.
const RUNTIME = (globalThis as unknown as { __SEDOC_API_BASE__?: string }).__SEDOC_API_BASE__
const BUILD   = (typeof process !== 'undefined' && process.env?.SEDOC_API_BASE) || ''

export const API_BASE = (RUNTIME || BUILD || 'https://app.vaultdms.example.com').replace(/\/+$/, '')
