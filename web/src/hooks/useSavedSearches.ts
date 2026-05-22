import { useQuery, useQueryClient } from '@tanstack/react-query'
import { listSavedSearches, createSavedSearch, deleteSavedSearch } from '@/api/savedSearches'
import { useAppMutation } from './useAppMutation'

// Wave 5 pattern 1: both mutations gained the default error toast.
// onSuccess invalidations preserved.

export function useSavedSearches() {
  return useQuery({ queryKey: ['saved-searches'], queryFn: listSavedSearches })
}

export function useCreateSavedSearch() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: createSavedSearch,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-searches'] }),
    defaultErrorMessage: 'Could not save search',
  })
}

export function useDeleteSavedSearch() {
  const qc = useQueryClient()
  return useAppMutation({
    mutationFn: deleteSavedSearch,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-searches'] }),
    defaultErrorMessage: 'Could not delete saved search',
  })
}
