// AdminGuard is a lightweight wrapper that short-circuits rendering
// for non-admin users. Used by the /admin/platform/* routes.
//
// The _authenticated parent already ensures the user is logged in
// (see routes/_authenticated.tsx); this guard adds the admin/owner
// role gate. We don't redirect — rendering an explanatory panel is
// nicer UX than a silent 404 and matches the pattern in the
// permissions matrix route.

import type { ReactNode } from 'react'
import { useAuthStore } from '@/store/authStore'

interface AdminGuardProps {
  children: ReactNode
}

export function AdminGuard({ children }: AdminGuardProps) {
  const user = useAuthStore((s) => s.user)
  const role = user?.role

  if (role !== 'admin' && role !== 'owner') {
    return (
      <div
        role="alert"
        className="mx-auto mt-16 max-w-md rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-6 text-center"
      >
        <h2 className="text-base font-semibold">Admin access required</h2>
        <p className="mt-2 text-sm text-[var(--color-text-secondary)]">
          This area is only visible to users with the admin or owner role.
        </p>
      </div>
    )
  }

  return <>{children}</>
}
