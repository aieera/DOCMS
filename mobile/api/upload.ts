import { api } from './client'

export async function initiateUpload(params: { filename: string; mime_type: string; size_bytes: number }) {
  const { data } = await api.post('/storage/uploads/initiate', params)
  return data
}
