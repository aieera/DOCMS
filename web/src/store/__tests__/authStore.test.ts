import { describe, it, expect, beforeEach } from 'vitest'
import { useAuthStore } from '@/store/authStore'
import { fixtures } from '@/test/mocks/handlers'

describe('authStore', () => {
  beforeEach(() => {
    // Zustand stores are singletons — reset between tests so state doesn't
    // leak. `logout()` brings the store back to its initial shape.
    useAuthStore.getState().logout()
  })

  it('starts unauthenticated', () => {
    const s = useAuthStore.getState()
    expect(s.isAuthenticated).toBe(false)
    expect(s.user).toBeNull()
    expect(s.tenantId).toBeNull()
  })

  it('login() sets user + tenant + authenticated flag', () => {
    useAuthStore.getState().login(fixtures.user, fixtures.tenantId)
    const s = useAuthStore.getState()
    expect(s.isAuthenticated).toBe(true)
    expect(s.user?.email).toBe('admin@acme.local')
    expect(s.tenantId).toBe(fixtures.tenantId)
  })

  it('logout() clears every field', () => {
    useAuthStore.getState().login(fixtures.user, fixtures.tenantId)
    useAuthStore.getState().logout()
    const s = useAuthStore.getState()
    expect(s.isAuthenticated).toBe(false)
    expect(s.user).toBeNull()
    expect(s.tenantId).toBeNull()
  })

  it('updateUser() merges partials without clobbering unset fields', () => {
    useAuthStore.getState().login(fixtures.user, fixtures.tenantId)
    useAuthStore.getState().updateUser({ display_name: 'Renamed' })
    const s = useAuthStore.getState()
    expect(s.user?.display_name).toBe('Renamed')
    expect(s.user?.email).toBe('admin@acme.local') // untouched
    expect(s.user?.role).toBe('owner')
  })

  it('updateUser() on a logged-out store is a no-op', () => {
    useAuthStore.getState().updateUser({ display_name: 'Anon' })
    expect(useAuthStore.getState().user).toBeNull()
  })
})
