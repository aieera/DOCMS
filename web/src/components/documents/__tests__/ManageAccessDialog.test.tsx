import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { ManageAccessDialog } from '../ManageAccessDialog'

vi.mock('sonner', () => ({
  toast: { error: vi.fn(), success: vi.fn(), message: vi.fn() },
}))

vi.mock('@/api/client', () => ({
  readErrorMessage: (e: unknown) => {
    const d = (e as { response?: { data?: { error?: string } } })?.response?.data
    return d?.error ?? null
  },
}))

const getPermissionsMock = vi.fn()
const grantPermissionMock = vi.fn()
const revokePermissionMock = vi.fn()
const checkPermissionMock = vi.fn()

vi.mock('@/api/permissions', () => ({
  getPermissions: (rt: string, rid: string) => getPermissionsMock(rt, rid),
  grantPermission: (...args: unknown[]) => grantPermissionMock(...args),
  revokePermission: (rt: string, rid: string, pid: string) => revokePermissionMock(rt, rid, pid),
  checkPermission: (action: string, rt: string, rid: string) => checkPermissionMock(action, rt, rid),
}))

const getUsersMock = vi.fn()
vi.mock('@/api/admin', () => ({
  getUsers: (params?: Record<string, string>) => getUsersMock(params),
}))

const listGroupsMock = vi.fn()
vi.mock('@/api/groups', () => ({
  listGroups: () => listGroupsMock(),
}))

function wrap(children: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

const aliceId = 'user-alice'
const bobId = 'user-bob'
const usersFixture = [
  { id: aliceId, email: 'alice@example.com', display_name: 'Alice', mfa_enabled: false },
  { id: bobId, email: 'bob@example.com', display_name: 'Bob', mfa_enabled: false },
]

beforeEach(() => {
  getPermissionsMock.mockReset()
  grantPermissionMock.mockReset()
  revokePermissionMock.mockReset()
  checkPermissionMock.mockReset()
  getUsersMock.mockReset()
  listGroupsMock.mockReset()
  // Sensible defaults.
  getUsersMock.mockResolvedValue({ items: usersFixture, total_count: 2 })
  listGroupsMock.mockResolvedValue([])
})

function defaultProps(overrides: Partial<React.ComponentProps<typeof ManageAccessDialog>> = {}) {
  return {
    open: true,
    onOpenChange: vi.fn(),
    resourceType: 'document' as const,
    resourceId: 'doc-1',
    resourceTitle: 'Q1 Report',
    workspaceId: 'ws-1',
    folderId: 'fld-1',
    ...overrides,
  }
}

describe('<ManageAccessDialog>', () => {
  // SECURITY: while the per-resource checkPermission query is still
  // in flight, isAdmin must evaluate to FALSE (strict ===true gate).
  // Any flash of the add form before the check returns would be a
  // leak — a non-admin briefly seeing a grantable surface. Fail-
  // closed: undefined (loading) and false both yield "not admin".
  it('fail-closed-during-loading: the add form must not render while checkPermission is pending', async () => {
    // checkPermission stays pending — never resolves during this test.
    let resolveCheck: (v: boolean) => void = () => {}
    checkPermissionMock.mockImplementation(
      () => new Promise<boolean>((res) => { resolveCheck = res }),
    )
    // direct + workspace + folder grants all resolve fast so the
    // outer isLoading flips false; only canManage is pending.
    getPermissionsMock.mockResolvedValue([])

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    // Give React Query + RAF time to flush every other resolution.
    // If the gate is fail-open, the form would mount in this window.
    await waitFor(() => {
      // The skeleton is the loading sentinel; while canManage is
      // pending, isLoading stays true and only skeletons render.
      // (Even if isLoading flipped, isAdmin would still be false.)
      expect(screen.queryByTestId('add-access-form')).not.toBeInTheDocument()
      expect(screen.queryByTestId('grant-access-submit')).not.toBeInTheDocument()
      expect(screen.queryByTestId('capability-select')).not.toBeInTheDocument()
    })

    // Belt-and-braces: poll a few times to catch any delayed render.
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 10))
      expect(screen.queryByTestId('add-access-form')).not.toBeInTheDocument()
    }

    // Once the per-resource check resolves to true, the form appears.
    resolveCheck(true)
    await screen.findByTestId('add-access-form')
  })

  // SECURITY: with the backend reporting "not an admin on this
  // resource", the UI must hide every affordance that could expand
  // access. No add form, no capability options exposed to the DOM,
  // no remove buttons on existing direct grants.
  it('cannot-grant-when-not-admin: hides the add form, capability selector, and remove buttons', async () => {
    checkPermissionMock.mockResolvedValue(false)
    getPermissionsMock.mockImplementation((rt: string) => {
      if (rt === 'document') {
        return Promise.resolve([
          {
            id: 'g1', resource_type: 'document', resource_id: 'doc-1',
            principal_type: 'user', principal_id: aliceId,
            capability: 'edit', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      return Promise.resolve([])
    })

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    expect(await screen.findByTestId('manage-access-readonly-notice')).toBeInTheDocument()
    // Add form, capability selector, expiry input, and submit are all absent.
    expect(screen.queryByTestId('add-access-form')).not.toBeInTheDocument()
    expect(screen.queryByTestId('capability-select')).not.toBeInTheDocument()
    expect(screen.queryByTestId('grant-access-submit')).not.toBeInTheDocument()
    expect(screen.queryByTestId('grant-expires-input')).not.toBeInTheDocument()
    // Existing direct grant renders but with no per-row capability
    // selector and no Remove button — both gated by isAdmin.
    await screen.findByTestId(`direct-access-${aliceId}`)
    expect(screen.queryByTestId(`direct-capability-${aliceId}`)).not.toBeInTheDocument()
    expect(screen.queryByTestId(`remove-access-${aliceId}`)).not.toBeInTheDocument()
  })

  it('hides the add form and renders a view-only notice when the caller is not admin', async () => {
    checkPermissionMock.mockResolvedValue(false)
    getPermissionsMock.mockResolvedValue([])

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    expect(await screen.findByTestId('manage-access-readonly-notice')).toBeInTheDocument()
    expect(screen.queryByTestId('add-access-form')).not.toBeInTheDocument()
  })

  it('renders the add form when the caller has admin and a user is selectable', async () => {
    checkPermissionMock.mockResolvedValue(true)
    // Direct grants empty, so all fixture users are pickable.
    getPermissionsMock.mockImplementation((rt: string, rid: string) => {
      if (rt === 'document' && rid === 'doc-1') return Promise.resolve([])
      return Promise.resolve([])
    })

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    expect(await screen.findByTestId('add-access-form')).toBeInTheDocument()
    expect(screen.getByTestId('grant-access-submit')).toBeDisabled()
  })

  it('shows direct grants with the user display name and an inline capability selector', async () => {
    checkPermissionMock.mockResolvedValue(true)
    getPermissionsMock.mockImplementation((rt: string) => {
      if (rt === 'document') {
        return Promise.resolve([
          {
            id: 'g1', resource_type: 'document', resource_id: 'doc-1',
            principal_type: 'user', principal_id: aliceId,
            capability: 'edit', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      return Promise.resolve([])
    })

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    const row = await screen.findByTestId(`direct-access-${aliceId}`)
    // Row mounts before usersQuery resolves, swapping "Unknown user"
    // for "Alice". Wait for the swap before the synchronous asserts.
    await within(row).findByText('Alice')
    expect(within(row).getByText('alice@example.com')).toBeInTheDocument()
    const select = within(row).getByTestId(`direct-capability-${aliceId}`) as HTMLSelectElement
    expect(select.value).toBe('edit')
  })

  it('renders inherited grants from the workspace with a "From workspace" badge', async () => {
    checkPermissionMock.mockResolvedValue(true)
    getPermissionsMock.mockImplementation((rt: string) => {
      if (rt === 'document') return Promise.resolve([])
      if (rt === 'workspace') {
        return Promise.resolve([
          {
            id: 'w1', resource_type: 'workspace', resource_id: 'ws-1',
            principal_type: 'user', principal_id: bobId,
            capability: 'view', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      return Promise.resolve([])
    })

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    // Wait for the users query to resolve so the row swaps "Unknown user"
    // for "Bob"; principalLabel does the lookup from the loaded list.
    await screen.findByText('Bob')
    const row = screen.getByTestId(`inherited-access-workspace-${bobId}`)
    expect(within(row).getByText(/from workspace/i)).toBeInTheDocument()
  })

  it('Remove confirmation surfaces residual access when the user is also on the workspace', async () => {
    checkPermissionMock.mockResolvedValue(true)
    getPermissionsMock.mockImplementation((rt: string) => {
      if (rt === 'document') {
        return Promise.resolve([
          {
            id: 'g1', resource_type: 'document', resource_id: 'doc-1',
            principal_type: 'user', principal_id: aliceId,
            capability: 'edit', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      if (rt === 'workspace') {
        return Promise.resolve([
          {
            id: 'w1', resource_type: 'workspace', resource_id: 'ws-1',
            principal_type: 'user', principal_id: aliceId,
            capability: 'view', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      return Promise.resolve([])
    })

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    const removeBtn = await screen.findByTestId(`remove-access-${aliceId}`)
    fireEvent.click(removeBtn)

    const dlg = await screen.findByRole('alertdialog')
    // The dialog mounts before usersQuery resolves; the description
    // updates from "Unknown user…" to "Alice…" on the next render.
    await waitFor(() => {
      expect(dlg).toHaveTextContent(/alice will still have viewer access from the workspace/i)
    })
  })

  it('Remove confirmation falls back to "lose direct access" copy when the user has no parent grant', async () => {
    checkPermissionMock.mockResolvedValue(true)
    getPermissionsMock.mockImplementation((rt: string) => {
      if (rt === 'document') {
        return Promise.resolve([
          {
            id: 'g1', resource_type: 'document', resource_id: 'doc-1',
            principal_type: 'user', principal_id: aliceId,
            capability: 'edit', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      return Promise.resolve([])
    })

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    const removeBtn = await screen.findByTestId(`remove-access-${aliceId}`)
    fireEvent.click(removeBtn)

    const dlg = await screen.findByRole('alertdialog')
    await waitFor(() => {
      expect(dlg).toHaveTextContent(/alice will lose direct access/i)
    })
  })

  it('revoke-removes-access: confirming Remove calls revokePermission with the principal id', async () => {
    checkPermissionMock.mockResolvedValue(true)
    revokePermissionMock.mockResolvedValue(undefined)
    getPermissionsMock.mockImplementation((rt: string) => {
      if (rt === 'document') {
        return Promise.resolve([
          {
            id: 'g1', resource_type: 'document', resource_id: 'doc-1',
            principal_type: 'user', principal_id: aliceId,
            capability: 'edit', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      return Promise.resolve([])
    })

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    await screen.findByTestId(`direct-access-${aliceId}`)
    fireEvent.click(screen.getByTestId(`remove-access-${aliceId}`))

    const dlg = await screen.findByRole('alertdialog')
    const confirm = within(dlg).getByRole('button', { name: /remove access/i })
    fireEvent.click(confirm)

    await waitFor(() => expect(revokePermissionMock).toHaveBeenCalledWith('document', 'doc-1', aliceId))
  })

  // SECURITY: when the backend rejects a grant (403/423/etc.), the UI
  // must surface the actual reason — never claim success. The dialog
  // pipes the error through readErrorMessage so 'forbidden' envelopes
  // become user-visible.
  it('backend rejection on grant surfaces an error toast (does not silently succeed)', async () => {
    const { toast } = await import('sonner')
    checkPermissionMock.mockResolvedValue(true)
    getPermissionsMock.mockResolvedValue([])
    grantPermissionMock.mockRejectedValue({
      response: { status: 403, data: { error: 'forbidden: requires admin on document' } },
    })

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    await screen.findByTestId('add-access-form')
    // Open the user combobox (button text starts with the placeholder)
    // and select Alice. The capability <select> also has combobox role,
    // hence the targeting by the user-picker placeholder text.
    fireEvent.click(screen.getByText(/pick a user/i))
    const opt = await screen.findByText(/alice \(alice@example\.com\)/i)
    fireEvent.click(opt)
    fireEvent.click(screen.getByTestId('grant-access-submit'))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith('forbidden: requires admin on document')
    })
    // Sanity: success was not falsely reported.
    expect(toast.success).not.toHaveBeenCalledWith('Access granted')
  })

  // SECURITY: optimistic revoke pulls the row from the cached list
  // BEFORE the mutation resolves, so the UI is instant. If the
  // mutation then fails, the cache is rolled back to the snapshot.
  // We hold the mutation's promise open and assert the DOM is
  // already updated.
  it('optimistic revoke: row disappears before the network call resolves', async () => {
    checkPermissionMock.mockResolvedValue(true)
    getPermissionsMock.mockImplementation((rt: string) => {
      if (rt === 'document') {
        return Promise.resolve([
          {
            id: 'g1', resource_type: 'document', resource_id: 'doc-1',
            principal_type: 'user', principal_id: aliceId,
            capability: 'edit', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      return Promise.resolve([])
    })
    // Controllable promise: stays pending until we resolve it.
    let resolveRevoke: () => void = () => {}
    revokePermissionMock.mockImplementation(() => new Promise<void>((res) => { resolveRevoke = res }))

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    await screen.findByTestId(`direct-access-${aliceId}`)
    fireEvent.click(screen.getByTestId(`remove-access-${aliceId}`))

    const dlg = await screen.findByRole('alertdialog')
    const confirm = within(dlg).getByRole('button', { name: /remove access/i })
    fireEvent.click(confirm)

    // The row should already be gone — even though revokePermission's
    // promise is still pending — because onMutate pruned the cache.
    await waitFor(() => {
      expect(screen.queryByTestId(`direct-access-${aliceId}`)).not.toBeInTheDocument()
    })
    expect(revokePermissionMock).toHaveBeenCalledTimes(1)

    // Now let the network resolve; the row should stay gone.
    resolveRevoke()
    await waitFor(() => expect(screen.queryByTestId(`direct-access-${aliceId}`)).not.toBeInTheDocument())
  })

  // SECURITY: rollback path — when the mutation fails, the
  // optimistic update must be reverted so the UI reflects what the
  // server says is still there.
  it('optimistic revoke rolls back when the server rejects the request', async () => {
    const { toast } = await import('sonner')
    checkPermissionMock.mockResolvedValue(true)
    getPermissionsMock.mockImplementation((rt: string) => {
      if (rt === 'document') {
        return Promise.resolve([
          {
            id: 'g1', resource_type: 'document', resource_id: 'doc-1',
            principal_type: 'user', principal_id: aliceId,
            capability: 'edit', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      return Promise.resolve([])
    })
    revokePermissionMock.mockRejectedValue({
      response: { status: 423, data: { error: 'locked: legal hold prevents revocation' } },
    })

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    await screen.findByTestId(`direct-access-${aliceId}`)
    fireEvent.click(screen.getByTestId(`remove-access-${aliceId}`))

    const dlg = await screen.findByRole('alertdialog')
    fireEvent.click(within(dlg).getByRole('button', { name: /remove access/i }))

    // After the rejection settles, the row should reappear (rolled
    // back) and the error message should surface.
    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith('locked: legal hold prevents revocation')
    })
    await waitFor(() => {
      expect(screen.getByTestId(`direct-access-${aliceId}`)).toBeInTheDocument()
    })
  })

  // inherited-vs-direct rendering — direct grants use the mutable
  // testid prefix, inherited grants use the inherited prefix with a
  // source badge. Same principal on both scopes renders in BOTH lists.
  it('inherited-vs-direct rendering: same user with grants at both scopes appears in both lists with distinct testids', async () => {
    checkPermissionMock.mockResolvedValue(true)
    getPermissionsMock.mockImplementation((rt: string) => {
      if (rt === 'document') {
        return Promise.resolve([
          {
            id: 'd1', resource_type: 'document', resource_id: 'doc-1',
            principal_type: 'user', principal_id: aliceId,
            capability: 'admin', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      if (rt === 'workspace') {
        return Promise.resolve([
          {
            id: 'w1', resource_type: 'workspace', resource_id: 'ws-1',
            principal_type: 'user', principal_id: aliceId,
            capability: 'view', granted_by: 'u-admin', granted_at: '2026-05-22T00:00:00Z',
          },
        ])
      }
      return Promise.resolve([])
    })

    render(wrap(<ManageAccessDialog {...defaultProps()} />))

    // Direct = mutable
    const direct = await screen.findByTestId(`direct-access-${aliceId}`)
    expect(within(direct).queryByTestId(`direct-capability-${aliceId}`)).toBeInTheDocument()
    // Inherited = read-only with a "From workspace" badge
    const inherited = await screen.findByTestId(`inherited-access-workspace-${aliceId}`)
    expect(within(inherited).getByText(/from workspace/i)).toBeInTheDocument()
    // No capability selector or remove button on the inherited row.
    expect(within(inherited).queryByRole('combobox')).not.toBeInTheDocument()
  })
})
