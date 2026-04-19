import { api } from './client'

export async function login(email: string, password: string) {
  const { data } = await api.post('/auth/login', { email, password })
  return data
}
