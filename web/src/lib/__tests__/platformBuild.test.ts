// Pre-sale item 01/03: /admin/platform pages (cross-tenant support
// search, db-info with the exact Postgres build string and roadmap
// matrix, load-tests) rendered in full for any tenant Owner who typed
// the URL. The API is platform-admin-gated server-side; the PAGES are
// operator tooling and must 404 in customer builds. They exist only
// when the build sets VITE_PLATFORM_ADMIN=true.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { isNotFound } from '@tanstack/react-router'
import { requirePlatformBuild } from '@/lib/platformBuild'

afterEach(() => vi.unstubAllEnvs())

describe('requirePlatformBuild', () => {
  it('throws notFound when the build is not a platform build', () => {
    vi.stubEnv('VITE_PLATFORM_ADMIN', '')
    try {
      requirePlatformBuild()
      throw new Error('expected notFound to be thrown')
    } catch (e) {
      expect(isNotFound(e)).toBe(true)
    }
  })

  it('passes in an operator build', () => {
    vi.stubEnv('VITE_PLATFORM_ADMIN', 'true')
    expect(() => requirePlatformBuild()).not.toThrow()
  })
})
