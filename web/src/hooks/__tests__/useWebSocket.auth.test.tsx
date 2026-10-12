// CB-3 / CB-6 (client half): the collaboration /ws endpoint requires an
// auth frame {type:'auth', token} as the FIRST frame (else it closes 4001
// after 10 s), re-checks access every 60 s, and:
//   - closes 4001 (invalid/expired session) or 4403 (access lost): the
//     client re-authenticates, then reconnects once — never a tight loop;
//   - closes 4503 (permission source down) or 1012 (restart): back off
//     and reconnect;
//   - closes 4400 (invalid room id): permanent — no reconnect, toast;
//   - sends {type:'subscription_revoked', doc_id}: drop that subscription
//     and never resubscribe automatically.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'

const toastError = vi.fn()
vi.mock('sonner', () => ({ toast: { error: (...a: unknown[]) => toastError(...a) } }))
const getCurrentUser = vi.fn()
vi.mock('@/api/auth', () => ({ getCurrentUser: (...a: unknown[]) => getCurrentUser(...a) }))

import { useWebSocket } from '@/hooks/useWebSocket'
import { useAuthStore } from '@/store/authStore'

class FakeWS {
  static instances: FakeWS[] = []
  static OPEN = 1
  readyState = 0
  sent: unknown[] = []
  onopen: (() => void) | null = null
  onmessage: ((e: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: ((e: { code: number; reason?: string }) => void) | null = null
  constructor(public url: string) { FakeWS.instances.push(this) }
  send(d: string) { this.sent.push(JSON.parse(d)) }
  close() { this.readyState = 3 }
  // test helpers
  open() { this.readyState = 1; this.onopen?.() }
  recv(m: unknown) { this.onmessage?.({ data: JSON.stringify(m) }) }
  drop(code: number) { this.readyState = 3; this.onclose?.({ code }) }
}

const last = () => FakeWS.instances[FakeWS.instances.length - 1]

function mount(opts: Parameters<typeof useWebSocket>[1] = {}) {
  const onMessage = vi.fn()
  const r = renderHook(() => useWebSocket(onMessage, { enabled: true, getToken: () => 'tok-1', ...opts }))
  return { onMessage, ...r }
}

beforeEach(() => {
  vi.useFakeTimers()
  FakeWS.instances = []
  vi.stubGlobal('WebSocket', FakeWS)
  toastError.mockClear()
  getCurrentUser.mockReset().mockResolvedValue({ id: 'u', tenant_id: 't' })
  useAuthStore.setState({
    user: { id: 'u', email: 'a@x', role: 'member', tenant_id: 't' } as never,
    tenantId: 't', isAuthenticated: true, isHydrating: false, hydrationPromise: null,
  })
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('useWebSocket — /ws auth protocol', () => {
  it('sends the auth frame first on open, then subscribes after auth_ok', () => {
    mount({ docId: 'd-1' })
    act(() => last().open())
    expect(last().sent[0]).toEqual({ type: 'auth', token: 'tok-1' })
    expect(last().sent).toHaveLength(1)
    act(() => last().recv({ type: 'auth_ok', user_id: 'u' }))
    expect(last().sent[1]).toEqual({ type: 'subscribe', doc_id: 'd-1' })
  })

  it('does not connect without a token', () => {
    mount({ getToken: () => null })
    expect(FakeWS.instances).toHaveLength(0)
  })

  it('4403: re-authenticates, then reconnects once — not a tight loop', async () => {
    mount({ docId: 'd-1' })
    act(() => last().open())
    act(() => last().drop(4403))
    expect(FakeWS.instances).toHaveLength(1) // no immediate reconnect
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(getCurrentUser).toHaveBeenCalledTimes(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(FakeWS.instances).toHaveLength(2)
    // A second auth loss right away gives up instead of looping.
    act(() => last().open())
    act(() => last().drop(4403))
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
    expect(FakeWS.instances).toHaveLength(2)
  })

  it('4001 with a dead session stops (no reconnect)', async () => {
    getCurrentUser.mockRejectedValue({ response: { status: 401 } })
    mount()
    act(() => last().open())
    act(() => last().drop(4001))
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
    expect(FakeWS.instances).toHaveLength(1)
  })

  it('4503 backs off and reconnects', async () => {
    mount()
    act(() => last().open())
    act(() => last().drop(4503))
    await act(async () => { await vi.advanceTimersByTimeAsync(500) })
    expect(FakeWS.instances).toHaveLength(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000) })
    expect(FakeWS.instances).toHaveLength(2)
    expect(getCurrentUser).not.toHaveBeenCalled()
  })

  it('4400 is permanent: no reconnect, one toast', async () => {
    mount({ docId: 'not-a-uuid' })
    act(() => last().open())
    act(() => last().drop(4400))
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
    expect(FakeWS.instances).toHaveLength(1)
    expect(toastError).toHaveBeenCalledTimes(1)
  })

  it('subscription_revoked drops the doc and never resubscribes, even after reconnect', async () => {
    const onRevoked = vi.fn()
    const { result } = mount({ docId: 'd-1', onRevoked })
    act(() => last().open())
    act(() => last().recv({ type: 'auth_ok' }))
    act(() => last().recv({ type: 'subscription_revoked', doc_id: 'd-1', error: 'no access to document' }))
    expect(onRevoked).toHaveBeenCalledWith('d-1')
    expect(result.current.revoked).toBe(true)
    act(() => last().drop(1012))
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000) })
    expect(FakeWS.instances).toHaveLength(2)
    act(() => last().open())
    act(() => last().recv({ type: 'auth_ok' }))
    expect(last().sent).toEqual([{ type: 'auth', token: 'tok-1' }])
  })

  it('stops delivering doc events after revocation', () => {
    const { onMessage } = mount({ docId: 'd-1' })
    act(() => last().open())
    act(() => last().recv({ type: 'auth_ok' }))
    act(() => last().recv({ type: 'comment_created', doc_id: 'd-1' }))
    expect(onMessage).toHaveBeenCalledTimes(1)
    act(() => last().recv({ type: 'subscription_revoked', doc_id: 'd-1' }))
    act(() => last().recv({ type: 'comment_created', doc_id: 'd-1' }))
    act(() => last().recv({ type: 'presence_update', doc_id: 'd-1', users: [] }))
    expect(onMessage).toHaveBeenCalledTimes(1)
  })

  it('closing on unmount does not reconnect', async () => {
    const { unmount } = mount()
    act(() => last().open())
    unmount()
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(FakeWS.instances).toHaveLength(1)
  })
})
