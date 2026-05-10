import { create } from 'zustand'
import { persist } from 'zustand/middleware'

interface UIState {
  sidebarCollapsed: boolean
  viewMode: 'grid' | 'table'
  activeWorkspaceId: string | null
  activeFolderId: string | null
  toggleSidebar: () => void
  setViewMode: (mode: 'grid' | 'table') => void
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
      activeWorkspaceId: null,
      activeFolderId: null,
      toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
      setViewMode: (mode) => set({ viewMode: mode }),
      setActiveWorkspace: (id) => set({ activeWorkspaceId: id, activeFolderId: null }),
      setActiveFolder: (id) => set({ activeFolderId: id }),
    }),
    { name: 'vaultdms-ui' },
  ),
)
