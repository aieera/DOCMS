import { ReactNode } from 'react'
import { render, RenderOptions } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

// A small wrapper every test uses so React Query and (eventually) router
// providers are available. Making a fresh QueryClient per test avoids
// stale cache bleed.
export function renderWithProviders(ui: ReactNode, opts?: RenderOptions) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
      mutations: { retry: false },
    },
  })
  return {
    client,
    ...render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>, opts),
  }
}
