import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider, createRouter } from '@tanstack/react-router'
import { Toaster } from 'react-hot-toast'
import { Toaster as SonnerToaster } from 'sonner'
import { ThemeProvider, useTheme } from '@/components/layout/theme-provider'
import { routeTree } from './routeTree.gen'
import './styles/globals.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 60_000, retry: 1 },
  },
})

const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register { router: typeof router }
}

// Sonner needs to know the resolved theme so its toast surfaces match
// the rest of the app instead of always rendering the system colour.
function ThemedSonner() {
  const { resolved } = useTheme()
  return (
    <SonnerToaster
      theme={resolved}
      richColors
      closeButton
      position="bottom-right"
      toastOptions={{ classNames: { toast: 'border border-border' } }}
    />
  )
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
        {/* react-hot-toast (legacy) and sonner (canonical) coexist
            during the migration. Phase 2 will retarget call sites to
            sonner and remove react-hot-toast. */}
        <Toaster position="top-right" toastOptions={{ duration: 4000, style: { borderRadius: '8px', fontSize: '14px' } }} />
        <ThemedSonner />
      </QueryClientProvider>
    </ThemeProvider>
  </React.StrictMode>,
)
