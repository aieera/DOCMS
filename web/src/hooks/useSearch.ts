import { useQuery } from '@tanstack/react-query'
import { search, autocomplete } from '@/api/search'

export function useSearch(body: Record<string, unknown>, enabled = true) {
  return useQuery({
    queryKey: ['search', body],
    queryFn: () => search(body),
    enabled,
    staleTime: 30_000,
  })
}

export function useAutocomplete(q: string) {
  return useQuery({
    queryKey: ['autocomplete', q],
    queryFn: () => autocomplete(q),
    enabled: q.length >= 2,
    staleTime: 10_000,
  })
}
