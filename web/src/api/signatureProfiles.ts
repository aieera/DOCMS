import { api } from './client'

// Wave 15.4 — Saved signature profiles.

export type SignatureKind = 'draw' | 'upload' | 'typed'

export interface SignatureProfile {
  id: string
  name: string
  kind: SignatureKind
  font_style?: string
  is_default: boolean
  created_at: string
  updated_at: string
}

export async function listProfiles() {
  const { data } = await api.get<{ profiles: SignatureProfile[] }>('/signatures/profiles')
  return data.profiles ?? []
}

export async function createProfile(input: {
  name: string
  kind: SignatureKind
  font_style?: string
  imageBase64: string
  setDefault?: boolean
}) {
  const { data } = await api.post<SignatureProfile>('/signatures/profiles', {
    name: input.name,
    kind: input.kind,
    font_style: input.font_style,
    image_base64: input.imageBase64,
    set_default: !!input.setDefault,
  })
  return data
}

export async function renameProfile(id: string, name: string) {
  const { data } = await api.patch<SignatureProfile>(`/signatures/profiles/${id}`, { name })
  return data
}

export async function setDefaultProfile(id: string) {
  const { data } = await api.patch<SignatureProfile>(`/signatures/profiles/${id}`, {
    set_default: true,
  })
  return data
}

export async function deleteProfile(id: string) {
  await api.delete(`/signatures/profiles/${id}`)
}

// profileImageURL returns the signed endpoint for a profile image.
// No headers / query manipulation — the browser fetches through the
// same authenticated session.
export function profileImageURL(id: string): string {
  return `/api/v1/signatures/profiles/${id}/image`
}
