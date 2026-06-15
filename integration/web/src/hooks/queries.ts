import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiGet, apiPostEmpty, apiPostJSON, uploadFile } from "../lib/api";
import type {
  CustomerTree,
  DocumentDetail,
  DocumentList,
  ReviewList,
  SyncLogList,
} from "../lib/types";

export function useCustomerTree(ref: string) {
  return useQuery({
    queryKey: ["tree", ref],
    queryFn: () => apiGet<CustomerTree>(`/files/customers/${encodeURIComponent(ref)}/tree`),
    enabled: !!ref,
  });
}

export function useFolderDocuments(ref: string, folderId: string | undefined) {
  return useQuery({
    queryKey: ["docs", ref, folderId],
    queryFn: () =>
      apiGet<DocumentList>(
        `/files/customers/${encodeURIComponent(ref)}/folders/${folderId}/documents`,
      ),
    enabled: !!ref && !!folderId,
  });
}

export function useDocumentDetail(documentId: string | undefined) {
  return useQuery({
    queryKey: ["doc", documentId],
    queryFn: () => apiGet<DocumentDetail>(`/files/documents/${documentId}`),
    enabled: !!documentId,
  });
}

export function useUpload(ref: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { file: File; onProgress?: (p: number) => void }) =>
      uploadFile(ref, args.file, args.onProgress),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["tree", ref] });
      qc.invalidateQueries({ queryKey: ["docs", ref] });
    },
  });
}

export function useRestoreVersion(documentId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (versionId: string) =>
      apiPostEmpty(`/files/documents/${documentId}/versions/${versionId}/restore`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["doc", documentId] }),
  });
}

export function useReviewQueue(status: string) {
  return useQuery({
    queryKey: ["review", status],
    queryFn: () => apiGet<ReviewList>(`/files/review-queue?status=${encodeURIComponent(status)}`),
  });
}

export interface ResolveBody {
  decision: "new_version" | "new_document" | "reject";
  target_document_id?: string;
  external_key?: string;
  notes?: string;
}

export function useResolveReview() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: string; body: ResolveBody }) =>
      apiPostJSON(`/files/review-queue/${args.id}/resolve`, args.body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["review"] }),
  });
}

export function useSyncLog(status: string) {
  return useQuery({
    queryKey: ["sync", status],
    queryFn: () =>
      apiGet<SyncLogList>(`/files/sync/log${status ? `?status=${encodeURIComponent(status)}` : ""}`),
    refetchInterval: 5000, // live-ish dashboard
  });
}

export function useRetrySync() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiPostEmpty(`/files/sync/retry/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sync"] }),
  });
}
