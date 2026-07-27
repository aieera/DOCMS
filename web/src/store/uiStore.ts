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
  toggleSidebar: () => void
  setViewMode: (mode: 'grid' | 'table') => void
  setWorkspacesViewMode: (mode: 'grid' | 'list') => void
  setBrowserViewMode: (mode: 'grid' | 'list') => void
  setActiveWorkspace: (id: string | null) => void
  setActiveFolder: (id: string | null) => void
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
      toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
      setViewMode: (mode) => set({ viewMode: mode }),
      setWorkspacesViewMode: (mode) => set({ workspacesViewMode: mode }),
      setBrowserViewMode: (mode) => set({ browserViewMode: mode }),
      setActiveWorkspace: (id) => set({ activeWorkspaceId: id, activeFolderId: null }),
      setActiveFolder: (id) => set({ activeFolderId: id }),
    }),
    { name: 'vaultdms-ui' },
  ),
)
