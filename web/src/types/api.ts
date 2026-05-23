export interface User {
  id: string
  tenant_id?: string
  email: string
  display_name: string
  // compliance_officer was added in Wave 11.2 (legal-hold flow); the
  // type lagged the backend. Widening here fixes pre-existing TS
  // errors in the doc detail page comparing role === 'compliance_officer'.
  role: 'owner' | 'admin' | 'member' | 'guest' | 'compliance_officer'
  // ADR 0069 — platform-admin is membership in the platform_admins
  // table, not a user role. The /auth/me handler joins against it
  // so the frontend can gate cross-tenant features (federated
  // search, support search). Unset for every regular tenant user.
  is_platform_admin?: boolean
  status?: 'active' | 'suspended' | 'deactivated'
  avatar_url?: string
  mfa_enabled: boolean
  // ADR 0106 — UI-language preference. Always populated from /auth/me;
  // defaults to 'en' on the server when a user has never set it.
  locale?: 'en' | 'ar'
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
  // The workspace creator — also the canonical "owner" in our single-
  // owner model. Mutable via POST /workspaces/{id}/transfer-ownership.
  created_by?: string
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
  // Phase 5 — business retention waiver (NOT legal hold). When true,
  // the retention sweep skips this document. reason carries the
  // justification recorded with the exemption event.
  retention_exempt?: boolean
  retention_exempt_reason?: string
}

export interface Version {
  id: string
  document_id: string
  version_number: number
  change_summary?: string
  // Phase 9 — optional human-friendly label set after upload
  // ("Q1 final", "Approved for legal review"). Empty string when
  // unset. Mutable via PATCH /api/v1/documents/{doc}/versions/{ver}/label.
  label?: string
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
