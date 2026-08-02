import { describe, it, expect } from 'vitest'
import {
  adminPathAllowsComplianceOfficer,
  COMPLIANCE_OFFICER_ADMIN_PATHS,
} from '@/routes/_authenticated'

// The allowlist mirrors the Go handlers that grant compliance_officer
// read access (auto_tag, ocr_quality, compliance_pii, anomaly). These
// tests pin the guard's shape: exact hub match, prefix matches for the
// pages, and — critically — that '/admin' being listed does NOT open
// every /admin/* path.
describe('adminPathAllowsComplianceOfficer', () => {
  it('allows the hub itself, exact match only', () => {
    expect(adminPathAllowsComplianceOfficer('/admin')).toBe(true)
  })

  it.each([
    '/admin/ocr',
    '/admin/pii-scanning',
    '/admin/tagging',
    '/admin/intelligence/anomalies',
  ])('allows %s and its subpaths', (p) => {
    expect(adminPathAllowsComplianceOfficer(p)).toBe(true)
    expect(adminPathAllowsComplianceOfficer(p + '/sub')).toBe(true)
  })

  // Legacy aliases redirect into allowed pages, but this guard runs before
  // the stub's beforeLoad — each alias must be allowed or the role gets
  // bounced to '/' instead of redirected.
  it.each([
    '/admin/pii',
    '/admin/tags',
    '/admin/intelligence/ocr-config',
    '/admin/intelligence/anomaly-reports',
  ])('allows the legacy alias %s so its redirect stub can run', (p) => {
    expect(adminPathAllowsComplianceOfficer(p)).toBe(true)
  })

  it.each([
    '/admin/identity',
    '/admin/tenant-settings',
    '/admin/ai',
    '/admin/ingestion',
    '/admin/intelligence/routing-rules',
    '/admin/intelligence/filing-analytics',
    '/admin/tenant/encryption',
    // hub is exact-match: a sibling path must not ride the '/admin' entry
    '/admin/anything-else',
  ])('denies %s', (p) => {
    expect(adminPathAllowsComplianceOfficer(p)).toBe(false)
  })

  it('does not treat lookalike prefixes as matches', () => {
    // '/admin/ocr' must not admit '/admin/ocr-extras' (startsWith trap)
    expect(adminPathAllowsComplianceOfficer('/admin/ocrx')).toBe(false)
    expect(adminPathAllowsComplianceOfficer('/admin/pii-scanning-legacy')).toBe(false)
  })

  it('keeps the documented list in sync with itself', () => {
    for (const p of COMPLIANCE_OFFICER_ADMIN_PATHS) {
      expect(adminPathAllowsComplianceOfficer(p)).toBe(true)
    }
  })
})
