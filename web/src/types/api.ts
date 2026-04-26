export interface User {
  id: string
  tenant_id?: string
  email: string
  display_name: string
  role: 'owner' | 'admin' | 'member' | 'guest'
  status?: 'active' | 'suspended' | 'deactivated'
  avatar_url?: string
  mfa_enabled: boolean
  created_at?: string
  last_login_at?: string | null
}

export interface Workspace {
  id: string
  name: string
  description?: string
  document_count: number
  member_count: number
  created_at: string
  updated_at?: string
}

export interface Folder {
  id: string
  workspace_id: string
  parent_id?: string
  name: string
  path: string
  document_count: number
  children_count: number
}

export interface Document {
  id: string
  tenant_id: string
  workspace_id: string
  folder_id?: string
  title: string
  description?: string
  document_class?: string
  lifecycle_state: string
  mime_type: string
  size_bytes: number
  version_count: number
  tags: string[]
  created_by: string
  created_by_name: string
  created_at: string
  updated_at?: string
  has_thumbnail: boolean
  thumbnail_url?: string
  region_pin?: string
  // Wave 17 §9.3: under_legal_hold is the boolean fast-path the
  // backend's enforcement layer reads; hold_count composes overlapping
  // holds (folder-hold + doc-hold = 2). Either truthy = doc is held.
  under_legal_hold?: boolean
  hold_count?: number
}

export interface Version {
  id: string
  document_id: string
  version_number: number
  change_summary?: string
  size_bytes: number
  created_by: string
  created_by_name: string
  created_at: string
}

export interface SearchResult {
  results: SearchHit[]
  facets: Record<string, FacetBucket[]>
  total_count: number
  page_token?: string
  latency_ms: number
  search_mode: string
}

export interface SearchHit {
  document_id: string
  title: string
  description?: string
  highlights?: Record<string, string[]>
  document_class?: string
  lifecycle_state: string
  workspace_id: string
  folder_id?: string
  tags: string[]
  created_by_name: string
  created_at: string
  size_bytes: number
  mime_type: string
  has_thumbnail: boolean
  score: number
}

export interface FacetBucket {
  value: string
  count: number
}

export interface Notification {
  id: string
  type: string
  title: string
  body: string
  read: boolean
  created_at: string
  resource_type?: string
  resource_id?: string
}

export interface UploadSession {
  upload_id: string
  presigned_put_url: string
  storage_bucket: string
  storage_key: string
  expires_at: string
  deduplicated?: boolean
  existing_blob_id?: string
}

export interface PaginatedResponse<T> {
  items: T[]
  total_count: number
  page_token?: string
}

export interface AIAnswer {
  answer: string
  sources: { document_id: string; chunk_index: number }[]
  model: string
  cost_usd: number
  elapsed_ms: number
}
