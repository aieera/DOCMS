import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

// Theme mode the user picks. 'system' defers to the OS-level
// prefers-color-scheme media query, which keeps the UI in sync if
// the user toggles their OS theme mid-session.
export type ThemeMode = 'light' | 'dark' | 'system'

interface ThemeContextValue {
  // The user's stored preference — what shows as "selected" in the
  // theme toggle UI. May be 'system'.
  mode: ThemeMode
  // The actual rendered theme — always 'light' or 'dark'. Use this
  // for any code that needs to branch on the visible state (e.g. an
  // image swap or chart color).
  resolved: 'light' | 'dark'
  setMode: (mode: ThemeMode) => void
}

const STORAGE_KEY = 'vaultdms-theme'
const ThemeContext = createContext<ThemeContextValue | null>(null)

function applyClass(theme: 'light' | 'dark') {
  const root = document.documentElement
  root.classList.toggle('dark', theme === 'dark')
  // color-scheme tells the browser to render native UI (form
  // controls, scrollbars where the custom CSS doesn't override) in
  // the matching theme — eliminates the bright-flash on dropdowns.
  root.style.colorScheme = theme
}

export function ThemeProvider({ children, defaultMode = 'system' }: { children: ReactNode; defaultMode?: ThemeMode }) {
  const [mode, setModeState] = useState<ThemeMode>(() => {
    if (typeof window === 'undefined') return defaultMode
    const stored = window.localStorage.getItem(STORAGE_KEY) as ThemeMode | null
    return stored ?? defaultMode
  })
  const [resolved, setResolved] = useState<'light' | 'dark'>('light')

  useEffect(() => {
    const mql = window.matchMedia('(prefers-color-scheme: dark)')
    const compute = (): 'light' | 'dark' => (mode === 'system' ? (mql.matches ? 'dark' : 'light') : mode)
    const apply = () => { const r = compute(); setResolved(r); applyClass(r) }
    apply()
    window.localStorage.setItem(STORAGE_KEY, mode)
    if (mode === 'system') {
      mql.addEventListener('change', apply)
      return () => mql.removeEventListener('change', apply)
    }
  }, [mode])

  const setMode = (m: ThemeMode) => setModeState(m)
  return (
    <ThemeContext.Provider value={{ mode, resolved, setMode }}>
      {children}
    </ThemeContext.Provider>
  )
}

export function useTheme() {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error('useTheme must be used inside <ThemeProvider>')
  return ctx
}
