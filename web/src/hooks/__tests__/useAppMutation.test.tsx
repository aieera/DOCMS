// Wave 5 pattern 1 — useAppMutation invariants. Every migration in
// Turn 2 depends on these holding: the wrapper ADDS a default
// onError; it must NEVER replace an existing onSuccess or fire its
// default in addition to a caller-provided onError.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { toast } from 'sonner'
import type { ReactNode } from 'react'

import { useAppMutation } from '../useAppMutation'

// Mock sonner so we can observe toast.error invocations without
// touching the DOM. The real Sonner component is irrelevant here —
// we only care that .error() was called (or wasn't).
vi.mock('sonner', () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}))

const wrapper = ({ children }: { children: ReactNode }) => {
  // Fresh QueryClient per test so cached state can't leak between
  // cases. retry: false so a failing mutation surfaces immediately.
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

beforeEach(() => {
  vi.mocked(toast.error).mockReset()
  vi.mocked(toast.success).mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('useAppMutation — default onError', () => {
  it('toasts the server error message when no onError is passed', async () => {
    const { result } = renderHook(
      () =>
        useAppMutation({
          mutationFn: async () => {
            // axios-shaped error so readErrorMessage finds .response.data.error
            throw {
              response: { status: 400, data: { error: 'name already taken' } },
            }
          },
        }),
      { wrapper },
    )
    act(() => {
      result.current.mutate(undefined as void)
    })
    await waitFor(() => expect(result.current.isError).toBe(true))
    expect(toast.error).toHaveBeenCalledWith('Name already taken')
  })

  it('falls back to defaultErrorMessage when the server message is missing', async () => {
    const { result } = renderHook(
      () =>
        useAppMutation({
          mutationFn: async () => {
            throw new Error('opaque network failure')
          },
          defaultErrorMessage: 'Could not save settings',
        }),
      { wrapper },
    )
    act(() => {
      result.current.mutate(undefined as void)
    })
    await waitFor(() => expect(result.current.isError).toBe(true))
    expect(toast.error).toHaveBeenCalledWith('Could not save settings')
  })

  it('uses a generic message when neither server message nor defaultErrorMessage exist', async () => {
    const { result } = renderHook(
      () =>
        useAppMutation({
          mutationFn: async () => {
            throw new Error('no parseable envelope')
          },
        }),
      { wrapper },
    )
    act(() => {
      result.current.mutate(undefined as void)
    })
    await waitFor(() => expect(result.current.isError).toBe(true))
    expect(toast.error).toHaveBeenCalledWith('Something went wrong')
  })
})

describe('useAppMutation — caller-provided onError override', () => {
  it('runs the caller onError INSTEAD of the default (not in addition)', async () => {
    const callerOnError = vi.fn()
    const { result } = renderHook(
      () =>
        useAppMutation({
          mutationFn: async () => {
            throw { response: { status: 500, data: { error: 'boom' } } }
          },
          onError: callerOnError,
        }),
      { wrapper },
    )
    act(() => {
      result.current.mutate(undefined as void)
    })
    await waitFor(() => expect(result.current.isError).toBe(true))
    expect(callerOnError).toHaveBeenCalledTimes(1)
    // The crucial assertion: default toast did NOT fire.
    expect(toast.error).not.toHaveBeenCalled()
  })
})

describe('useAppMutation — onSuccess preservation (the migration invariant)', () => {
  // Every Option-B migration in Turn 2 depends on onSuccess running
  // unchanged. These cases pin that contract: success path runs
  // the caller's onSuccess and never touches toast.error.

  it('fires onSuccess on a successful mutation', async () => {
    const onSuccess = vi.fn()
    const { result } = renderHook(
      () =>
        useAppMutation({
          mutationFn: async () => 'ok',
          onSuccess,
        }),
      { wrapper },
    )
    act(() => {
      result.current.mutate(undefined as void)
    })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(onSuccess).toHaveBeenCalledTimes(1)
    // react-query v5 passes (data, variables, context, MutateOptions);
    // we only care that data + variables arrive intact — exact arg
    // count is library trivia, not the invariant we're pinning.
    expect(onSuccess.mock.calls[0][0]).toBe('ok')
    expect(onSuccess.mock.calls[0][1]).toBeUndefined()
    // Default error toast must NOT fire on the success path.
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('forwards onSuccess return value / data unchanged', async () => {
    const onSuccess = vi.fn()
    const { result } = renderHook(
      () =>
        useAppMutation<{ id: string }, unknown, { name: string }>({
          mutationFn: async (vars) => ({ id: `id-of-${vars.name}` }),
          onSuccess,
        }),
      { wrapper },
    )
    act(() => {
      result.current.mutate({ name: 'workspace-A' })
    })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(onSuccess.mock.calls[0][0]).toEqual({ id: 'id-of-workspace-A' })
    expect(onSuccess.mock.calls[0][1]).toEqual({ name: 'workspace-A' })
    expect(result.current.data).toEqual({ id: 'id-of-workspace-A' })
  })

  it('preserves onMutate + onSettled alongside the injected default onError', async () => {
    // Cache-side effects (optimistic updates, invalidations) live in
    // onMutate/onSettled in some sites. The wrapper must not drop them.
    const onMutate = vi.fn()
    const onSettled = vi.fn()
    const { result } = renderHook(
      () =>
        useAppMutation({
          mutationFn: async () => {
            throw { response: { data: { error: 'denied' } } }
          },
          onMutate,
          onSettled,
        }),
      { wrapper },
    )
    act(() => {
      result.current.mutate(undefined as void)
    })
    await waitFor(() => expect(result.current.isError).toBe(true))
    expect(onMutate).toHaveBeenCalledTimes(1)
    expect(onSettled).toHaveBeenCalledTimes(1)
    // Default toast still fired (no override).
    expect(toast.error).toHaveBeenCalledWith('Denied')
  })

  it('preserves onSuccess + caller onError together (no toast on either path)', async () => {
    // Site shape: caller supplies BOTH onSuccess (for cache work)
    // AND onError (for a bespoke message). Wrapper must call both,
    // and the default toast must stay silent.
    const onSuccess = vi.fn()
    const onError = vi.fn()
    const { result } = renderHook(
      () =>
        useAppMutation({
          mutationFn: async () => 'ok',
          onSuccess,
          onError,
        }),
      { wrapper },
    )
    act(() => {
      result.current.mutate(undefined as void)
    })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(onSuccess).toHaveBeenCalledTimes(1)
    expect(onError).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
  })
})
