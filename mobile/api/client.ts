import axios from 'axios'
import { useAuthStore } from '../store/authStore'

export const API_BASE = process.env.EXPO_PUBLIC_API_URL || 'http://localhost:8080/api/v1'

export const api = axios.create({ baseURL: API_BASE })

api.interceptors.request.use((config) => {
  const { token } = useAuthStore.getState()
  if (token) config.headers.Authorization = `Bearer ${token}`
  // No X-Tenant-ID header: the gateway strips client-supplied identity
  // headers (kong.yaml request-transformer) — tenant comes from the
  // session the backend resolves from the bearer.
  return config
})

api.interceptors.response.use((r) => r, (error) => {
  // Only a 401 on a request that actually carried our token means the
  // session is dead. A pre-hydration request (no Authorization header)
  // 401s too — wiping SecureStore for that would destroy a perfectly
  // valid session on every deep-link cold start (review finding).
  if (error.response?.status === 401 && error.config?.headers?.Authorization) {
    void useAuthStore.getState().logout()
  }
  return Promise.reject(error)
})
