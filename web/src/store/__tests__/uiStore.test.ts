import { describe, it, expect, beforeEach } from 'vitest'

import { useUIStore } from '../uiStore'

const persisted = () => JSON.parse(window.localStorage.getItem('vaultdms-ui') ?? '{}').state ?? {}

describe('uiStore notifications panel flag', () => {
  beforeEach(() => {
    window.localStorage.clear()
    useUIStore.setState({ notificationsOpen: false, sidebarCollapsed: false })
  })

  it('starts closed', () => {
    expect(useUIStore.getState().notificationsOpen).toBe(false)
  })

  it('opens and closes through the store', () => {
    useUIStore.getState().openNotifications()
    expect(useUIStore.getState().notificationsOpen).toBe(true)
    useUIStore.getState().closeNotifications()
    expect(useUIStore.getState().notificationsOpen).toBe(false)
  })

  it('never writes the panel flag to storage, so a reload does not reopen the panel', () => {
    useUIStore.getState().openNotifications()
    const state = persisted()
    expect(state).not.toHaveProperty('notificationsOpen')
  })

  it('still persists every other preference', () => {
    useUIStore.getState().toggleSidebar()
    useUIStore.getState().openNotifications()
    expect(Object.keys(persisted()).sort()).toEqual([
      'activeFolderId', 'activeWorkspaceId', 'browserViewMode', 'sidebarCollapsed', 'viewMode', 'workspacesViewMode',
    ])
    expect(persisted().sidebarCollapsed).toBe(true)
  })
})
