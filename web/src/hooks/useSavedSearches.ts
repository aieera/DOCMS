import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { listSavedSearches, createSavedSearch, deleteSavedSearch } from '@/api/savedSearches'

export function useSavedSearches() {
  return useQuery({ queryKey: ['saved-searches'], queryFn: listSavedSearches })
}

export function useCreateSavedSearch() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: createSavedSearch,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-searches'] }),
  })
}

export function useDeleteSavedSearch() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: deleteSavedSearch,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-searches'] }),
  })
}
