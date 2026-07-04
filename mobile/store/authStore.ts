// Session state, persisted in the platform keychain via expo-secure-store
// (§8.1's mobile analogue: the bearer NEVER touches AsyncStorage or any
// other plaintext storage — SecureStore is Keychain on iOS, EncryptedSharedPreferences
// on Android). An optional biometric gate (expo-local-authentication)
// covers unlocks on cold start; see app/index.tsx.
import { create } from 'zustand'
import * as SecureStore from 'expo-secure-store'
import { clearQueryCache } from '../lib/queryClient'

interface User { id: string; email: string; display_name: string; role: string; tenant_id: string }

const KEY_TOKEN = 'sedoc.session_token'
const KEY_USER = 'sedoc.user'
const KEY_TENANT = 'sedoc.tenant_id'
const KEY_BIOMETRIC = 'sedoc.biometric_unlock' // '1' | '0'

interface AuthState {
  user: User | null
  token: string | null
  tenantId: string | null
  isAuthenticated: boolean
  /** True once hydrate() has run — routing decisions wait for this. */
  hydrated: boolean
  /** User preference: require Face ID / fingerprint on app open. */
  biometricEnabled: boolean
  login: (user: User, token: string, tenantId: string) => Promise<void>
  logout: () => Promise<void>
  hydrate: () => Promise<void>
  setBiometricEnabled: (on: boolean) => Promise<void>
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  token: null,
  tenantId: null,
  isAuthenticated: false,
  hydrated: false,
  biometricEnabled: false,

  login: async (user, token, tenantId) => {
    if (typeof token !== 'string' || token === '') {
      // A malformed login response must fail loudly here — persisting
      // undefined made SecureStore reject and surfaced as a bogus
      // "invalid credentials" (review finding).
      throw new Error('auth: missing session token')
    }
    set({ user, token, tenantId, isAuthenticated: true })
    // Persist AFTER the in-memory set so the UI never waits on keychain IO.
    await Promise.all([
      SecureStore.setItemAsync(KEY_TOKEN, token),
      SecureStore.setItemAsync(KEY_USER, JSON.stringify(user)),
      SecureStore.setItemAsync(KEY_TENANT, tenantId ?? ''),
    ])
  },

  logout: async () => {
    set({ user: null, token: null, tenantId: null, isAuthenticated: false })
    await Promise.all([
      SecureStore.deleteItemAsync(KEY_TOKEN),
      SecureStore.deleteItemAsync(KEY_USER),
      SecureStore.deleteItemAsync(KEY_TENANT),
      // The persisted read cache holds the account's documents/tasks —
      // it must not survive into the next user's session.
      clearQueryCache(),
    ])
  },

  hydrate: async () => {
    try {
      const [token, userJSON, tenantId, biometric] = await Promise.all([
        SecureStore.getItemAsync(KEY_TOKEN),
        SecureStore.getItemAsync(KEY_USER),
        SecureStore.getItemAsync(KEY_TENANT),
        SecureStore.getItemAsync(KEY_BIOMETRIC),
      ])
      if (token && userJSON) {
        set({
          token,
          user: JSON.parse(userJSON) as User,
          tenantId: tenantId || null,
          isAuthenticated: true,
          biometricEnabled: biometric === '1',
          hydrated: true,
        })
        return
      }
      set({ biometricEnabled: biometric === '1', hydrated: true })
    } catch {
      // Corrupt/inaccessible keychain entry → treat as signed out. The
      // stale entries are overwritten on the next successful login.
      set({ hydrated: true })
    }
  },

  setBiometricEnabled: async (on) => {
    set({ biometricEnabled: on })
    await SecureStore.setItemAsync(KEY_BIOMETRIC, on ? '1' : '0')
  },
}))
