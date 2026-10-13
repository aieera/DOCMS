// CB-3: /yjs close codes. 4400 (invalid room id) is permanent with a
// toast; 4401 (no/expired session) and 4403 (tenant mismatch or view
// revoked mid-session) stop the reconnect loop; 4503 (permission source
// down) and 1012 (server restart) keep y-websocket's backoff reconnect.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'

const toastError = vi.fn()
vi.mock('sonner', () => ({ toast: { error: (...a: unknown[]) => toastError(...a) } }))

type Listener = (e: unknown) => void
interface FakeProvider {
  shouldConnect: boolean
  emit: (ev: string, e: unknown) => void
}
const { providers } = vi.hoisted(() => ({ providers: [] as FakeProvider[] }))
vi.mock('y-websocket', () => ({
  WebsocketProvider: class {
    shouldConnect = true
    wsconnected = false
    wsconnecting = true
    awareness = {}
    listeners = new Map<string, Listener[]>()
    constructor() { providers.push(this as unknown as FakeProvider) }
    on(ev: string, fn: Listener) { this.listeners.set(ev, [...(this.listeners.get(ev) ?? []), fn]) }
    off() {}
    destroy() {}
    emit(ev: string, e: unknown) { for (const fn of this.listeners.get(ev) ?? []) fn(e) }
  },
}))

import { useYDoc } from '@/lib/useYDoc'
import { useAuthStore } from '@/store/authStore'

const DOC = '0190c0de-0000-7000-8000-000000000001'

beforeEach(() => {
  providers.length = 0
  toastError.mockClear()
  useAuthStore.setState({
    user: { id: 'u', email: 'a@x', role: 'member', tenant_id: 't' } as never,
    tenantId: 't', isAuthenticated: true, isHydrating: false, hydrationPromise: null,
  })
})

function closeWith(code: number) {
  const { result } = renderHook(() => useYDoc(DOC))
  act(() => providers[0].emit('connection-close', { code }))
  return { result, provider: providers[0] }
}

describe('useYDoc close codes (CB-3)', () => {
  it('4400 stops reconnecting and toasts once', () => {
    const { result, provider } = closeWith(4400)
    expect(provider.shouldConnect).toBe(false)
    expect(result.current?.status).toBe('invalid_room')
    expect(toastError).toHaveBeenCalledTimes(1)
  })

  it('4403 (access revoked) stops reconnecting', () => {
    const { result, provider } = closeWith(4403)
    expect(provider.shouldConnect).toBe(false)
    expect(result.current?.status).toBe('access_revoked')
  })

  it('4401 stops reconnecting as auth_failed', () => {
    const { result, provider } = closeWith(4401)
    expect(provider.shouldConnect).toBe(false)
    expect(result.current?.status).toBe('auth_failed')
  })

  it.each([4503, 1012])('%i keeps the backoff reconnect', (code) => {
    const { provider } = closeWith(code)
    expect(provider.shouldConnect).toBe(true)
    expect(toastError).not.toHaveBeenCalled()
  })
})
