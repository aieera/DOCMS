import { useState } from "react";
import { Empty, ErrorBanner, Skeleton } from "../components/ui";
import { useResolveReview, useReviewQueue, type ResolveBody } from "../hooks/queries";
import type { ReviewItem } from "../lib/types";

// [E] Review Queue — admin triage of low-confidence /ingest reads SeDoc couldn't
// auto-route.
export function ReviewQueue() {
  const q = useReviewQueue("pending");
  if (q.isLoading) return <Skeleton rows={6} />;
  if (q.error) return <ErrorBanner error={q.error} />;
  const items = q.data?.items ?? [];
  if (items.length === 0) return <Empty>No items awaiting review. 🎉</Empty>;
  return (
    <div className="space-y-3">
      <h1 className="text-lg font-semibold">Review queue</h1>
      {items.map((it) => (
        <ReviewCard key={it.id} item={it} />
      ))}
    </div>
  );
}

function ReviewCard({ item }: { item: ReviewItem }) {
  const resolve = useResolveReview();
  const [docId, setDocId] = useState("");
  const [key, setKey] = useState(item.extracted_external_key);

  function act(body: ResolveBody) {
    resolve.mutate({ id: item.id, body });
  }

  return (
    <div className="rounded border p-3 text-sm">
      <div className="mb-2 flex items-center justify-between">
        <div>
          <span className="font-medium">{item.target_customer_ref || "—"}</span>
          <span className="ml-2 text-xs text-gray-500">
            reason: {item.reason} · confidence {(item.confidence * 100).toFixed(0)}%
          </span>
        </div>
        <span className="text-xs text-gray-400">{item.document_class || "unclassified"}</span>
      </div>
      {item.extracted_external_key && (
        <p className="mb-1 text-xs">
          extracted key: <span className="font-mono">{item.extracted_external_key}</span>
          {item.suggested_match_document_id && (
            <span className="ml-2 text-gray-500">suggested match: {item.suggested_match_document_id.slice(0, 8)}…</span>
          )}
        </p>
      )}
      {item.ocr_text && (
        <pre className="mb-2 max-h-24 overflow-auto rounded bg-gray-50 p-2 text-[11px] text-gray-600">{item.ocr_text}</pre>
      )}
      {resolve.error && <ErrorBanner error={resolve.error} />}
      <div className="flex flex-wrap items-center gap-2">
        <input
          aria-label="Target document id for new version"
          placeholder="target document id"
          value={docId}
          onChange={(e) => setDocId(e.target.value)}
          className="w-48 rounded border px-2 py-1 text-xs"
        />
        <button
          disabled={!docId || resolve.isPending}
          onClick={() => act({ decision: "new_version", target_document_id: docId })}
          className="rounded bg-brand px-2 py-1 text-xs text-white disabled:opacity-40"
        >
          New version of…
        </button>
        <input
          aria-label="External key for new document"
          placeholder="external key"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          className="w-40 rounded border px-2 py-1 text-xs"
        />
        <button
          disabled={resolve.isPending}
          onClick={() => act({ decision: "new_document", external_key: key })}
          className="rounded border border-brand px-2 py-1 text-xs text-brand disabled:opacity-40"
        >
          New document
        </button>
        <button
          disabled={resolve.isPending}
          onClick={() => act({ decision: "reject", notes: "rejected in review" })}
          className="rounded border border-red-300 px-2 py-1 text-xs text-red-700 disabled:opacity-40"
        >
          Reject
        </button>
      </div>
    </div>
  );
}
