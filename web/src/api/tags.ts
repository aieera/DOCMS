import { api } from './client'

export interface Tag {
  id: string
  name: string
  color: string
  document_count: number
}

// grpc-gateway wraps list responses as { tags: [...] }.
interface ListTagsResponse {
  tags?: Tag[]
}

export async function listTags(): Promise<Tag[]> {
  const { data } = await api.get<ListTagsResponse>('/tags')
  return data.tags ?? []
}

export async function createTag(input: { name: string; color: string }): Promise<Tag> {
  const { data } = await api.post<Tag>('/tags', input)
  return data
}

export async function deleteTag(id: string): Promise<void> {
  await api.delete(`/tags/${id}`)
}
