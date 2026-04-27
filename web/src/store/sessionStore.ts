// Session state store — drives the X-Session-Warning banner and the
// "session revoked for security reasons" full-screen modal. Kept
// separate from authStore because these flags are transient UI state,
// not identity.

import { create } from 'zustand'

interface SessionState {
  bindingWarning: boolean
  revoked: boolean
  showWarning: () => void
  dismissWarning: () => void
  showRevoked: () => void
  reset: () => void
}

export const useSessionStore = create<SessionState>((set) => ({
  bindingWarning: false,
  revoked: false,
  showWarning: () => set({ bindingWarning: true }),
  dismissWarning: () => set({ bindingWarning: false }),
  showRevoked: () => set({ revoked: true }),
  reset: () => set({ bindingWarning: false, revoked: false }),
}))
