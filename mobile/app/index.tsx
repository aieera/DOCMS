// Root gate — the app's single entry decision point:
//   1. hydrate the session from SecureStore (survives restarts),
//   2. optionally demand a biometric unlock (user preference),
//   3. route to the tabs (authed) or the login screen.
// Before this existed a cold start landed on an undefined route and the
// session died with the process.
import { useEffect, useState } from 'react'
import { View, Text, TouchableOpacity, ActivityIndicator, StyleSheet } from 'react-native'
import { Redirect } from 'expo-router'
import * as LocalAuthentication from 'expo-local-authentication'
import { useAuthStore } from '../store/authStore'
import { registerForPush } from '../lib/push'

type Gate = 'loading' | 'locked' | 'open'

export default function Index() {
  const { hydrated, isAuthenticated, biometricEnabled, hydrate } = useAuthStore()
  const [gate, setGate] = useState<Gate>('loading')

  useEffect(() => {
    if (!hydrated) void hydrate()
  }, [hydrated, hydrate])

  // Re-register the push token on every unlocked authed start — Expo
  // tokens rotate and the backend upsert is idempotent. Runs as an
  // effect (never during render, which double-fires under StrictMode
  // and pops the OS permission prompt at uncontrolled moments).
  useEffect(() => {
    if (hydrated && isAuthenticated && gate === 'open') void registerForPush()
  }, [hydrated, isAuthenticated, gate])

  useEffect(() => {
    if (!hydrated) return
    if (!isAuthenticated || !biometricEnabled) {
      setGate('open')
      return
    }
    void unlock()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hydrated, isAuthenticated, biometricEnabled])

  const unlock = async () => {
    setGate('loading')
    try {
      const capable = await LocalAuthentication.hasHardwareAsync()
      const enrolled = capable && (await LocalAuthentication.isEnrolledAsync())
      if (!enrolled) {
        // No biometrics enrolled on this device — the preference can't
        // be honored, so don't lock the user out of their own session.
        setGate('open')
        return
      }
      const res = await LocalAuthentication.authenticateAsync({
        promptMessage: 'Unlock SeDoc',
        cancelLabel: 'Cancel',
      })
      setGate(res.success ? 'open' : 'locked')
    } catch {
      setGate('locked')
    }
  }

  if (!hydrated || gate === 'loading') {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" color="#1E40AF" />
      </View>
    )
  }
  if (gate === 'locked') {
    return (
      <View style={styles.center}>
        <Text style={styles.title}>SeDoc is locked</Text>
        <TouchableOpacity style={styles.button} onPress={() => void unlock()}>
          <Text style={styles.buttonText}>Unlock</Text>
        </TouchableOpacity>
      </View>
    )
  }
  return isAuthenticated ? <Redirect href="/(tabs)" /> : <Redirect href="/(auth)/login" />
}

const styles = StyleSheet.create({
  center: { flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: '#f8fafc', gap: 16 },
  title: { fontSize: 18, fontWeight: '700', color: '#1E40AF' },
  button: { height: 44, paddingHorizontal: 32, backgroundColor: '#1E40AF', borderRadius: 8, justifyContent: 'center' },
  buttonText: { color: '#fff', fontSize: 15, fontWeight: '600' },
})
