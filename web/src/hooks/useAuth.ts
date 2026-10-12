import { useAuthStore } from '@/store/authStore'
import * as authApi from '@/api/auth'
import { useQueryClient } from '@tanstack/react-query'
import { useUploadStore } from '@/store/uploadStore'

// Wave 5 pattern 5: useLogin was deleted in Turn 5 — it was orphaned
// (login.tsx drives all auth flows directly through
// tenant-fallback: doc-reference (next line quotes the deleted trap)
// finalizeLoginResult, see H-1) AND carried a `user.tenant_id ?? ''`
// fallback that would have re-introduced the empty-tenant-ID trap
// the moment any new code started consuming it. The handoff doc
// captures the deletion as a follow-up note in case a future
// caller appears; new login flows must route through
// lib/finalizeLogin.ts, not through a re-introduction of this hook.

// H-25: query keys carry no user/tenant discriminator, so logout must
// wipe the React Query cache and the upload store — otherwise the next
// user on this tab is served the previous user's workspaces, tasks and
// notifications from cache. The hard navigation then reloads the app so
// no module-level state survives either.
//
// Order matters: the server revoke is sent first (it rides on the
// cookie), local state is wiped immediately, and the revoke is given at
// most LOGOUT_REVOKE_TIMEOUT_MS before the navigation — a hanging request
// can never keep the previous user's data on screen.
const LOGOUT_REVOKE_TIMEOUT_MS = 3_000

export function useLogout() {
  const authLogout = useAuthStore((s) => s.logout)
  const qc = useQueryClient()
  return async () => {
    const revoke = authApi.logout().catch(() => {
      // Best-effort server revoke; local state is cleared regardless.
    })
    void qc.cancelQueries()
    qc.clear()
    useUploadStore.getState().reset()
    authLogout()
    let timer: ReturnType<typeof setTimeout> | undefined
    await Promise.race([
      revoke,
      new Promise<void>((resolve) => { timer = setTimeout(resolve, LOGOUT_REVOKE_TIMEOUT_MS) }),
    ])
    if (timer) clearTimeout(timer)
    window.location.href = '/login'
  }
}

export function useIsAuthenticated() {
  return useAuthStore((s) => s.isAuthenticated)
}

export function useCurrentUser() {
  return useAuthStore((s) => s.user)
}
