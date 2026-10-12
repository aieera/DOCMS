import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

vi.mock('@/api/auth', () => ({ logout: vi.fn().mockResolvedValue(undefined) }))

import * as authApi from '@/api/auth'
import { useLogout } from '@/hooks/useAuth'
import { useAuthStore } from '@/store/authStore'
import { useUploadStore } from '@/store/uploadStore'

// H-25: query keys carry no user/tenant discriminator, so the React
// Query cache and the upload store must be wiped on logout — otherwise
// the next user on the same tab sees the previous user's workspaces,
// tasks and notifications straight from cache.
let href = 'http://localhost:3000/'
beforeEach(() => {
  href = 'http://localhost:3000/'
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: {
      get href() { return href },
      set href(v: string) { href = v },
      assign: (v: string) => { href = v },
      replace: (v: string) => { href = v },
      pathname: '/',
      origin: 'http://localhost:3000',
    },
  })
  useAuthStore.setState({
    user: { id: 'u-1', email: 'a@x', role: 'owner', tenant_id: 't-1' } as never,
    tenantId: 't-1',
    isAuthenticated: true,
    isHydrating: false,
    hydrationPromise: null,
  })
})
afterEach(() => vi.clearAllMocks())

describe('useLogout (H-25)', () => {
  it('clears the query cache and upload store, then hard-navigates to /login', async () => {
    const qc = new QueryClient()
    qc.setQueryData(['workspaces'], [{ id: 'w-secret' }])
    qc.setQueryData(['notifications', 'unread'], 7)
    const file = new File(['x'], 'a.txt')
    useUploadStore.getState().addUpload({ id: 'up-1', file, progress: 10, status: 'uploading' })
    const pending = useUploadStore.getState().requestDuplicateDecision('a.txt', [])

    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useLogout(), { wrapper })
    await act(async () => {
      await result.current()
    })

    expect(authApi.logout).toHaveBeenCalledTimes(1)
    expect(qc.getQueryCache().getAll()).toHaveLength(0)
    expect(qc.getQueryData(['workspaces'])).toBeUndefined()
    expect(useUploadStore.getState().uploads.size).toBe(0)
    expect(useUploadStore.getState().duplicatePrompt).toBeNull()
    // A batch awaiting a duplicate decision is released (skipped), not stranded.
    await expect(pending).resolves.toBe(false)
    expect(useAuthStore.getState().isAuthenticated).toBe(false)
    // Hard navigation (full reload), not a client-side route change that
    // would keep in-memory module state alive.
    expect(href).toBe('/login')
  })

  it('still clears local state when the logout request fails', async () => {
    vi.mocked(authApi.logout).mockRejectedValueOnce(new Error('network'))
    const qc = new QueryClient()
    qc.setQueryData(['tasks'], [{ id: 't' }])
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useLogout(), { wrapper })
    await act(async () => {
      await result.current()
    })
    expect(qc.getQueryCache().getAll()).toHaveLength(0)
    expect(useAuthStore.getState().isAuthenticated).toBe(false)
    expect(href).toBe('/login')
  })
})
