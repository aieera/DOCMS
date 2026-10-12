// CB-3 / CB-6 (client half): the collaboration /ws endpoint requires an
// auth frame as the FIRST frame (else it closes 4001 after 10 s). The
// session is the HttpOnly dms_session cookie the browser sends on the
// upgrade; the frame is {type:'auth', mode:'cookie'} and carries NO
// token — the SPA never holds the session secret. The server re-checks
// access every 60 s, and:
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
  const r = renderHook(() => useWebSocket(onMessage, { enabled: true, ...opts }))
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
    expect(last().sent[0]).toEqual({ type: 'auth', mode: 'cookie' })
    expect(last().sent).toHaveLength(1)
    act(() => last().recv({ type: 'auth_ok', user_id: 'u' }))
    expect(last().sent[1]).toEqual({ type: 'subscribe', doc_id: 'd-1' })
  })

  it('never sends a session token in any frame, across reconnects', async () => {
    mount({ docId: 'd-1' })
    act(() => last().open())
    act(() => last().recv({ type: 'auth_ok' }))
    act(() => last().drop(4403))
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    act(() => last().open())
    const frames = FakeWS.instances.flatMap((w) => w.sent)
    expect(frames.length).toBeGreaterThan(0)
    for (const f of frames) {
      expect(f).not.toHaveProperty('token')
      expect(JSON.stringify(f)).not.toMatch(/session|token/i)
    }
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
    expect(last().sent).toEqual([{ type: 'auth', mode: 'cookie' }])
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

  // Review (round 1): `revoked` must not leak to the next document.
  it('resets revoked when the document changes, and subscribes to the new one', () => {
    const onMessage = vi.fn()
    const { result, rerender } = renderHook(
      ({ docId }) => useWebSocket(onMessage, { enabled: true, docId }),
      { initialProps: { docId: 'd-1' } },
    )
    act(() => last().open())
    act(() => last().recv({ type: 'auth_ok' }))
    act(() => last().recv({ type: 'subscription_revoked', doc_id: 'd-1' }))
    expect(result.current.revoked).toBe(true)
    rerender({ docId: 'd-2' })
    expect(result.current.revoked).toBe(false)
    act(() => last().open())
    act(() => last().recv({ type: 'auth_ok' }))
    expect(last().sent).toContainEqual({ type: 'subscribe', doc_id: 'd-2' })
    // Back to d-1 later: a fresh connection, not still flagged revoked.
    rerender({ docId: 'd-1' })
    expect(result.current.revoked).toBe(false)
  })

  // Review (round 1): auth_ok alone must not reset the backoff — a server
  // that accepts then drops at once would otherwise be hammered every 1 s.
  it('keeps backing off when connections drop right after auth_ok', async () => {
    mount()
    act(() => last().open())
    act(() => last().recv({ type: 'auth_ok' }))
    act(() => last().drop(4503))
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000) })
    expect(FakeWS.instances).toHaveLength(2)
    act(() => last().open())
    act(() => last().recv({ type: 'auth_ok' }))
    act(() => last().drop(4503))
    await act(async () => { await vi.advanceTimersByTimeAsync(1_500) })
    expect(FakeWS.instances).toHaveLength(2) // second wait is 2 s, not 1 s
    await act(async () => { await vi.advanceTimersByTimeAsync(600) })
    expect(FakeWS.instances).toHaveLength(3)
  })

  it('resets the backoff once a connection has stayed open for 60 s', async () => {
    mount()
    act(() => last().open())
    act(() => last().drop(4503))
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000) }) // 1 s
    act(() => last().open())
    act(() => last().drop(4503))
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000) }) // 2 s
    expect(FakeWS.instances).toHaveLength(3)
    act(() => last().open())
    act(() => last().recv({ type: 'auth_ok' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) }) // healthy
    act(() => last().drop(4503))
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000) })
    expect(FakeWS.instances).toHaveLength(4) // back to 1 s
  })
})
