import axios from 'axios'
import { useAuthStore } from '../store/authStore'

const API_BASE = process.env.EXPO_PUBLIC_API_URL || 'http://localhost:8080/api/v1'

export const api = axios.create({ baseURL: API_BASE })

api.interceptors.request.use((config) => {
  const { token, tenantId } = useAuthStore.getState()
  if (token) config.headers.Authorization = `Bearer ${token}`
  if (tenantId) config.headers['X-Tenant-ID'] = tenantId
  return config
})

api.interceptors.response.use((r) => r, (error) => {
  if (error.response?.status === 401) {
    useAuthStore.getState().logout()
  }
  return Promise.reject(error)
})
