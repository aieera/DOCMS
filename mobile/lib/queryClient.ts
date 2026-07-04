// Shared QueryClient + persisted-cache plumbing. Lives outside
// app/_layout so the auth store can clear the cache on logout without
// importing a route component (review finding: the persisted cache
// survived logout, showing the previous account's documents to the
// next user on a shared device).
import { QueryClient } from '@tanstack/react-query'
import { createAsyncStoragePersister } from '@tanstack/query-async-storage-persister'
import AsyncStorage from '@react-native-async-storage/async-storage'

export const CACHE_KEY = 'sedoc.query-cache'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      gcTime: 24 * 60 * 60 * 1000, // keep cached reads for a day
      retry: 1,
    },
  },
})

export const persister = createAsyncStoragePersister({
  storage: AsyncStorage,
  key: CACHE_KEY,
})

/** Wipe both the in-memory and persisted read cache (logout). */
export async function clearQueryCache(): Promise<void> {
  queryClient.clear()
  try {
    await AsyncStorage.removeItem(CACHE_KEY)
  } catch {
    // Cache clear is best-effort; the session is already gone.
  }
}
