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
  parent_folder_id?: string
  name: string
  path: string
  depth?: number
  document_count: number
  // Two server keys exist for back-compat. New endpoints emit
  // child_folder_count; legacy ones emit children_count. Components
  // read whichever is present.
  child_folder_count?: number
  children_count?: number
  // Visibility + owner_id land in Phase 2. Optional here so the
  // type compiles before the migration; the FE treats missing
  // visibility as 'shared'.
  visibility?: 'shared' | 'private'
  owner_id?: string
  created_at?: string
  updated_at?: string
}

export interface Document {
  id: string
  tenant_id: string
  workspace_id: string
  folder_id?: string
  title: string
  description?: string
  document_class?: string
  /** §8 classification-based access control. Sensitivity level gating access
   *  (unclassified|internal|confidential|restricted; empty = unset), plus the
   *  PII/PHI flags denormalised from the compliance scan. Drive the
   *  sensitivity badge. */
  security_classification?: '' | 'unclassified' | 'internal' | 'confidential' | 'restricted'
  has_phi?: boolean
  has_pii?: boolean
  lifecycle_state: string
  mime_type: string
  /** 'file' (default), 'note', or 'wiki'. Present on the createNote
   *  response; the gateway GET/List proto does not yet carry it, so treat
   *  as optional and fall back to mime (text/markdown) for recognition. */
  doc_type?: 'file' | 'note' | 'wiki'
  /** Sum of every version's blob size. Wire field is `total_size_bytes`
   *  on the document service (proto field 15). Empty/zero on legacy
   *  documents whose versions never recomputed the rollup. */
  total_size_bytes: number
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
  // Tenant-defined custom metadata (admin schema at
  // /api/v1/tenants/metadata-schema). Shape is whatever the tenant
  // declared; values are arbitrary JSON. Optional because legacy
  // docs predate the field. The doc-detail sidebar cross-references
  // the live schema to render labels + required markers.
  custom_metadata?: Record<string, unknown>
  // Active workflow summary, joined into the document payload at
  // read time. Absent (or null) when the document has no in-flight
  // workflow. DocumentCard + header render a status pill off this.
  workflow_instance?: {
    id: string
    definition_id: string
    definition_name: string
    status: 'pending' | 'running' | 'completed' | 'failed' | 'cancelled'
    current_step: number
    started_at: string
  } | null
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
  /** Optional: the backend omits this when the OpenSearch source field
   *  is missing or unparseable (search/internal/service mapHit) so the
   *  caller doesn't render the Go zero time as "2025 years ago". */
  created_at?: string
  /** Indexed on every new version (the indexer sets updated_at but not
   *  created_at yet), so it's the reliable timestamp when created_at comes
   *  back as the Go zero value. The result row falls back to this. */
  updated_at?: string
  size_bytes: number
  mime_type: string
  /** Total versions on the document (the index is one row per document;
   *  the search page's "N versions" expander fetches the list lazily). */
  version_count?: number
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
