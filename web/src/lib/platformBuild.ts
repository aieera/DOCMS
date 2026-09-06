// Pre-sale item 01/03 (2026-09-08 review): the /admin/platform pages —
// cross-tenant support search, db-info (exact Postgres build string +
// database roadmap matrix), load-tests — rendered in full for any tenant
// Owner who typed the URL. The support-search API is platform-admin
// enforced server-side (services/search federated.go, audited denials),
// but a customer administrator must never even SEE a screen that says the
// product can read across tenants. These pages are operator tooling:
// they exist only in builds made with VITE_PLATFORM_ADMIN=true and 404
// everywhere else.
import { notFound } from '@tanstack/react-router'

// Read at call time (not module load) so tests can stub the env and a
// dev .env carrying the flag doesn't bake into unrelated suites.
export function isPlatformBuild(): boolean {
  return import.meta.env.VITE_PLATFORM_ADMIN === 'true'
}

/** beforeLoad guard for /admin/platform routes. */
export function requirePlatformBuild(): void {
  if (!isPlatformBuild()) throw notFound()
}
