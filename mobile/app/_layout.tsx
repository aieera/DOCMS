import { useEffect } from 'react'
import { Stack } from 'expo-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StatusBar } from 'expo-status-bar'
import { startNetworkListener } from '../lib/uploadQueue'
import { registerBackgroundSync } from '../lib/backgroundSync'

const queryClient = new QueryClient()

export default function RootLayout() {
  useEffect(() => {
    // Foreground resume trigger: drain uploads whenever NetInfo sees
    // the device reconnect. Background task (registered separately
    // below) is the fallback when the app isn't in the foreground.
    const unsub = startNetworkListener()
    registerBackgroundSync().catch(() => { /* best-effort; ignore */ })
    return unsub
  }, [])

  return (
    <QueryClientProvider client={queryClient}>
      <StatusBar style="auto" />
      <Stack screenOptions={{ headerShown: false }} />
    </QueryClientProvider>
  )
}
