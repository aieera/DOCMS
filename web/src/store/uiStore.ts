import { create } from 'zustand'
import { persist } from 'zustand/middleware'

interface UIState {
  sidebarCollapsed: boolean
  viewMode: 'grid' | 'table'
  theme: 'light' | 'dark'
  activeWorkspaceId: string | null
  activeFolderId: string | null
  toggleSidebar: () => void
  setViewMode: (mode: 'grid' | 'table') => void
  toggleTheme: () => void
  setActiveWorkspace: (id: string | null) => void
  setActiveFolder: (id: string | null) => void
}

export const useUIStore = create<UIState>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,
      viewMode: 'grid',
      theme: 'light',
      activeWorkspaceId: null,
      activeFolderId: null,
      toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
      setViewMode: (mode) => set({ viewMode: mode }),
      toggleTheme: () =>
        set((s) => {
          const next = s.theme === 'light' ? 'dark' : 'light'
          document.documentElement.classList.toggle('dark', next === 'dark')
          return { theme: next }
        }),
      setActiveWorkspace: (id) => set({ activeWorkspaceId: id, activeFolderId: null }),
      setActiveFolder: (id) => set({ activeFolderId: id }),
    }),
    { name: 'vaultdms-ui' },
  ),
)
