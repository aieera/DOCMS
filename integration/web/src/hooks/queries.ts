import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useApiClient } from "../lib/apiContext";
import {
  ApiError,
  type BackfillDetail,
  type BackfillList,
  type CustomerTree,
  type DocumentDetail,
  type DocumentList,
  type ReviewItem,
  type ReviewList,
  type ReviewStatus,
  type SyncLogList,
  type SyncMetrics,
} from "../lib/types";

export function useCustomerTree(ref: string) {
  const api = useApiClient();
  return useQuery({
    queryKey: ["tree", ref],
    queryFn: () => api.get<CustomerTree>(`/files/customers/${encodeURIComponent(ref)}/tree`),
    enabled: !!ref,
  });
}

export function useFolderDocuments(ref: string, folderId: string | undefined) {
  const api = useApiClient();
  return useQuery({
    queryKey: ["docs", ref, folderId],
    queryFn: () =>
      api.get<DocumentList>(`/files/customers/${encodeURIComponent(ref)}/folders/${folderId}/documents`),
    enabled: !!ref && !!folderId,
  });
}

export function useDocumentDetail(documentId: string | undefined) {
  const api = useApiClient();
  return useQuery({
    queryKey: ["doc", documentId],
    queryFn: () => api.get<DocumentDetail>(`/files/documents/${documentId}`),
    enabled: !!documentId,
  });
}

export function useUpload(ref: string) {
  const api = useApiClient();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { file: File; onProgress?: (p: number) => void }) =>
      api.upload(ref, args.file, args.onProgress),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["tree", ref] });
      qc.invalidateQueries({ queryKey: ["docs", ref] });
    },
  });
}

export function useRestoreVersion(documentId: string) {
  const api = useApiClient();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (versionId: string) =>
      api.postEmpty(`/files/documents/${documentId}/versions/${versionId}/restore`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["doc", documentId] }),
  });
}

// useReviewQueue keyset-paginates the queue for a status (pending/resolved/
// rejected). "Load more" follows next_cursor.
export function useReviewQueue(status: ReviewStatus) {
  const api = useApiClient();
  return useInfiniteQuery({
    queryKey: ["review", status],
    queryFn: ({ pageParam }) =>
      api.get<ReviewList>(
        `/files/review-queue?status=${encodeURIComponent(status)}` +
          (pageParam ? `&cursor=${encodeURIComponent(pageParam)}` : ""),
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
}

// useReviewItem fetches one item WITH its OCR text (the list omits it) — for the
// per-row preview, fetched lazily when a row is expanded.
export function useReviewItem(id: string, enabled: boolean) {
  const api = useApiClient();
  return useQuery({
    queryKey: ["review-item", id],
    queryFn: () => api.get<ReviewItem>(`/files/review-queue/${encodeURIComponent(id)}`),
    enabled: enabled && !!id,
  });
}

export interface ResolveBody {
  decision: "new_version" | "new_document" | "reject";
  target_document_id?: string;
  external_key?: string;
  notes?: string;
}

export interface ResolveOutcome {
  alreadyResolved: boolean;
}

// useResolveReview applies a decision. A 409 (the item was resolved by someone
// else first) is NOT a failure: it resolves to {alreadyResolved:true} so the UI
// can show an info toast and refetch, rather than surfacing a red error.
export function useResolveReview() {
  const api = useApiClient();
  const qc = useQueryClient();
  return useMutation<ResolveOutcome, Error, { id: string; body: ResolveBody }>({
    mutationFn: async ({ id, body }) => {
      try {
        await api.postJSON(`/files/review-queue/${encodeURIComponent(id)}/resolve`, body);
        return { alreadyResolved: false };
      } catch (e) {
        if (e instanceof ApiError && e.status === 409) return { alreadyResolved: true };
        throw e;
      }
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["review"] }),
  });
}

export function useSyncLog(status: string) {
  const api = useApiClient();
  return useQuery({
    queryKey: ["sync", status],
    queryFn: () =>
      api.get<SyncLogList>(`/files/sync/log${status ? `?status=${encodeURIComponent(status)}` : ""}`),
    refetchInterval: 5000, // live-ish dashboard
  });
}

export function useRetrySync() {
  const api = useApiClient();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.postEmpty(`/files/sync/retry/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sync"] }),
  });
}

// useSyncMetrics polls the worker's operational metrics (throughput, backlog,
// DLQ, limiter state) via the BFF passthrough.
export function useSyncMetrics() {
  const api = useApiClient();
  return useQuery({
    queryKey: ["sync-metrics"],
    queryFn: () => api.get<SyncMetrics>("/files/sync/metrics"),
    refetchInterval: 5000,
  });
}

// useBackfillRuns lists recent backfill runs; polls so progress advances live.
export function useBackfillRuns() {
  const api = useApiClient();
  return useQuery({
    queryKey: ["backfill"],
    queryFn: () => api.get<BackfillList>("/files/sync/backfill"),
    refetchInterval: 5000,
  });
}

// useBackfillDetail loads one run + its per-item failures (polled while open).
export function useBackfillDetail(runId: string | undefined) {
  const api = useApiClient();
  return useQuery({
    queryKey: ["backfill", runId],
    queryFn: () => api.get<BackfillDetail>(`/files/sync/backfill/${runId}`),
    enabled: !!runId,
    refetchInterval: 3000,
  });
}

// useStartBackfill enqueues a backfill (optionally scoped to customer refs).
export function useStartBackfill() {
  const api = useApiClient();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (customerRefs: string[]) =>
      api.postJSON<{ run_id: string; status: string }>("/files/sync/backfill", {
        customer_refs: customerRefs,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["backfill"] }),
  });
}
