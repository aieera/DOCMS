import { create } from 'zustand'
import { persist } from 'zustand/middleware'

interface UIState {
  sidebarCollapsed: boolean
  viewMode: 'grid' | 'table'
  // Per-surface grid/list preference: the workspaces index and the
  // in-workspace folder browser toggle independently of the documents
  // list's `viewMode` above.
  workspacesViewMode: 'grid' | 'list'
  browserViewMode: 'grid' | 'list'
  activeWorkspaceId: string | null
  activeFolderId: string | null
  /** The header's notifications menu. Opened from outside the header too
   * (the dashboard's Unread card), so it lives here rather than in the
   * menu's local state. Never persisted — see `partialize` below. */
  notificationsOpen: boolean
  toggleSidebar: () => void
  setViewMode: (mode: 'grid' | 'table') => void
  setWorkspacesViewMode: (mode: 'grid' | 'list') => void
  setBrowserViewMode: (mode: 'grid' | 'list') => void
  setActiveWorkspace: (id: string | null) => void
  setActiveFolder: (id: string | null) => void
  openNotifications: () => void
  closeNotifications: () => void
}

// Theme state lives in <ThemeProvider> (localStorage-backed), not here.
// The legacy `theme`/`toggleTheme` fields were retired with the
// canonical layout migration.
export const useUIStore = create<UIState>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,
      viewMode: 'grid',
      workspacesViewMode: 'grid',
      browserViewMode: 'grid',
      activeWorkspaceId: null,
      activeFolderId: null,
      notificationsOpen: false,
      toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
      setViewMode: (mode) => set({ viewMode: mode }),
      setWorkspacesViewMode: (mode) => set({ workspacesViewMode: mode }),
      setBrowserViewMode: (mode) => set({ browserViewMode: mode }),
      setActiveWorkspace: (id) => set({ activeWorkspaceId: id, activeFolderId: null }),
      setActiveFolder: (id) => set({ activeFolderId: id }),
      openNotifications: () => set({ notificationsOpen: true }),
      closeNotifications: () => set({ notificationsOpen: false }),
    }),
    {
      name: 'vaultdms-ui',
      // Every preference persists except the notifications menu flag: it is
      // transient UI, and persisting it would reopen the menu on reload.
      partialize: ({ notificationsOpen: _notificationsOpen, ...persisted }) => persisted,
    },
  ),
)
