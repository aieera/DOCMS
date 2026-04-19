import { api } from './client'

export async function search(body: Record<string, unknown>) {
  const { data } = await api.post('/search', body)
  return data
}
