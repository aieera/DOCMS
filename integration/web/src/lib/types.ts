// Types mirroring the Files BFF responses (integration/internal/bff).

export interface Folder {
  id: string;
  name: string;
  parentFolderId?: string;
  documentCount: number;
  childFolderCount: number;
}

export interface CustomerTree {
  customer_ref: string;
  name: string;
  main_folder_id: string;
  folders: Folder[];
  next_page_token?: string;
}

export interface DocumentRow {
  id: string;
  title: string;
  folderId: string;
  documentClass: string;
  lifecycleState: string;
  currentVersionId: string;
  mimeType: string;
  totalSizeBytes: number;
  externalId: string;
  updatedAt: string;
}

export interface DocumentList {
  documents: DocumentRow[];
  pagination: { nextPageToken: string };
}

export interface Version {
  id: string;
  versionNumber: number;
  sizeBytes: number;
  mimeType: string;
  createdByName: string;
  createdAt: string;
  label: string;
}

export interface DocumentDetail {
  document: DocumentRow;
  versions: Version[];
}

export interface UploadResult {
  ingestion_item_id: string;
  status: string;
}

export type ReviewStatus = "pending" | "resolved" | "rejected";

export interface ReviewItem {
  id: string;
  ingestion_item_id: string;
  workspace_id: string;
  target_customer_ref: string;
  document_class: string;
  extracted_external_key: string;
  suggested_match_document_id?: string;
  confidence: number;
  reason: string;
  status: ReviewStatus;
  ocr_text?: string;
  created_at: string;
  // Resolution audit — present once an item leaves the pending state.
  resolution?: string; // new_version | new_document | reject
  resulting_document_id?: string;
  resolved_by?: string;
  resolved_at?: string;
  notes?: string;
}

export interface ReviewList {
  items: ReviewItem[];
  next_cursor?: string;
}

export interface SyncLogRow {
  id: string;
  erp_event_id: string;
  kind: string;
  customer_ref: string;
  status: string;
  attempts: number;
  correlation_id: string;
  last_error: string;
  updated_at: string;
}

export interface SyncLogList {
  items: SyncLogRow[];
}

export interface LimiterState {
  limit_per_sec: number;
  burst: number;
  tokens_available: number;
  wait_count: number;
  wait_total_ms: number;
}

export interface SyncMetrics {
  events_per_min: number;
  processed_total: number;
  http_429: number;
  backlog: number;
  dlq: number;
  limiter?: LimiterState;
}

export interface BackfillRun {
  id: string;
  status: string;
  source: string;
  source_arg: string;
  total: number;
  processed: number;
  failed: number;
  last_error: string;
  started_at?: string;
  finished_at?: string;
  created_at: string;
  updated_at: string;
}

export interface BackfillFailure {
  customer_ref: string;
  item: string;
  error: string;
  correlation_id: string;
  created_at: string;
}

export interface BackfillList {
  items: BackfillRun[];
}

export interface BackfillDetail {
  run: BackfillRun;
  failures: BackfillFailure[];
}

// ApiError carries the BFF/SeDoc correlation_id for support traceability.
export class ApiError extends Error {
  status: number;
  correlationId: string;
  constructor(status: number, message: string, correlationId: string) {
    super(message);
    this.status = status;
    this.correlationId = correlationId;
  }
}
