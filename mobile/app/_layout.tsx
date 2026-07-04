import { useEffect } from 'react'
import { View, ActivityIndicator } from 'react-native'
import { Stack } from 'expo-router'
import { PersistQueryClientProvider } from '@tanstack/react-query-persist-client'
import { StatusBar } from 'expo-status-bar'
import { queryClient, persister } from '../lib/queryClient'
import { wirePushNavigation } from '../lib/push'
import { useAuthStore } from '../store/authStore'

// Offline read cache (ADR 0117): query results (browse lists, search,
// document metadata, notifications, tasks) persist to AsyncStorage so
// the app renders the last-known state offline. AsyncStorage holds ONLY
// this display cache — the session token lives in SecureStore
// (store/authStore.ts), never here.

export default function RootLayout() {
  const { hydrated, hydrate } = useAuthStore()

  // Hydrate HERE, not in a leaf route: a cold start via a push tap or
  // scheme deep link mounts the target screen directly, and before the
  // session loads every query would 401 — and the interceptor would
  // then wipe the real keychain session (review finding).
  useEffect(() => {
    if (!hydrated) void hydrate()
  }, [hydrated, hydrate])

  useEffect(() => wirePushNavigation(), [])

  if (!hydrated) {
    return (
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: '#f8fafc' }}>
        <ActivityIndicator size="large" color="#1E40AF" />
      </View>
    )
  }

  return (
    <PersistQueryClientProvider
      client={queryClient}
      persistOptions={{ persister, maxAge: 24 * 60 * 60 * 1000 }}
    >
      <StatusBar style="auto" />
      <Stack screenOptions={{ headerShown: false }} />
    </PersistQueryClientProvider>
  )
}
