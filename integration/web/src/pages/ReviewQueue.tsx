import { useState } from "react";
import { Empty, ErrorBanner, Skeleton, StatusBadge } from "../components/ui";
import { toast } from "../components/toast";
import { useResolveReview, useReviewItem, useReviewQueue, type ResolveBody } from "../hooks/queries";
import type { ReviewItem, ReviewStatus } from "../lib/types";

const STATUSES: ReviewStatus[] = ["pending", "resolved", "rejected"];

// [E] Review Queue — admin triage of low-confidence /ingest reads SeDoc couldn't
// auto-route, plus a resolved/rejected history with who actioned each item.
export function ReviewQueue() {
  const [status, setStatus] = useState<ReviewStatus>("pending");
  const q = useReviewQueue(status);
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">Review queue</h1>
        <div className="flex gap-1" role="tablist" aria-label="Review status">
          {STATUSES.map((s) => (
            <button
              key={s}
              role="tab"
              aria-selected={status === s}
              onClick={() => setStatus(s)}
              className={`rounded px-2.5 py-1 text-xs capitalize ${
                status === s ? "bg-brand text-white" : "border text-gray-600 hover:bg-gray-50"
              }`}
            >
              {s}
            </button>
          ))}
        </div>
      </div>

      {q.isLoading ? (
        <Skeleton rows={6} />
      ) : q.error ? (
        <ErrorBanner error={q.error} />
      ) : items.length === 0 ? (
        <Empty>No {status} items. {status === "pending" ? "🎉" : null}</Empty>
      ) : (
        <>
          {items.map((it) => (
            <ReviewCard key={it.id} item={it} />
          ))}
          {q.hasNextPage && (
            <div className="text-center">
              <button
                onClick={() => q.fetchNextPage()}
                disabled={q.isFetchingNextPage}
                className="rounded border px-3 py-1 text-xs text-gray-600 hover:bg-gray-50 disabled:opacity-40"
              >
                {q.isFetchingNextPage ? "Loading…" : "Load more"}
              </button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function ReviewCard({ item }: { item: ReviewItem }) {
  const resolve = useResolveReview();
  const [docId, setDocId] = useState("");
  const [key, setKey] = useState(item.extracted_external_key);
  const [open, setOpen] = useState(false);
  const preview = useReviewItem(item.id, open);
  const isPending = item.status === "pending";

  function act(body: ResolveBody) {
    resolve.mutate(
      { id: item.id, body },
      {
        onSuccess: (out) => {
          if (out.alreadyResolved) {
            toast("This item was already resolved by someone else — list refreshed.", "info");
          } else {
            toast(`Resolved as ${body.decision.replace(/_/g, " ")}.`, "success");
          }
        },
      },
    );
  }

  return (
    <div className="rounded border p-3 text-sm">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="min-w-0">
          <span className="font-medium">{item.target_customer_ref || "—"}</span>
          <span className="ml-2 text-xs text-gray-500">
            reason: {item.reason} · confidence {(item.confidence * 100).toFixed(0)}%
          </span>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-xs text-gray-400">{item.document_class || "unclassified"}</span>
          <StatusBadge status={item.status} />
        </div>
      </div>

      {item.extracted_external_key && (
        <p className="mb-1 text-xs">
          extracted key: <span className="font-mono">{item.extracted_external_key}</span>
          {item.suggested_match_document_id && (
            <span className="ml-2 text-gray-500">
              suggested match: {item.suggested_match_document_id.slice(0, 8)}…
            </span>
          )}
        </p>
      )}

      {/* Resolution audit — who actioned a non-pending item, how, and when. */}
      {!isPending && (
        <div className="mb-2 rounded bg-gray-50 p-2 text-xs text-gray-600">
          <span className="font-medium capitalize">
            {(item.resolution || item.status).replace(/_/g, " ")}
          </span>
          {item.resolved_by && (
            <>
              {" · by "}
              <span className="font-mono">{item.resolved_by.slice(0, 8)}…</span>
            </>
          )}
          {item.resolved_at && <> · {new Date(item.resolved_at).toLocaleString()}</>}
          {item.resulting_document_id && (
            <>
              {" · doc "}
              <span className="font-mono">{item.resulting_document_id.slice(0, 8)}…</span>
            </>
          )}
          {item.notes && <div className="mt-1 italic">{item.notes}</div>}
        </div>
      )}

      {/* Lazy OCR preview. */}
      <button onClick={() => setOpen((v) => !v)} className="mb-2 text-xs text-brand underline">
        {open ? "Hide preview" : "Preview OCR"}
      </button>
      {open &&
        (preview.isLoading ? (
          <Skeleton rows={2} />
        ) : preview.error ? (
          <ErrorBanner error={preview.error} />
        ) : (
          <pre className="mb-2 max-h-40 overflow-auto rounded bg-gray-50 p-2 text-[11px] text-gray-600">
            {preview.data?.ocr_text || "(no OCR text)"}
          </pre>
        ))}

      {resolve.error && <ErrorBanner error={resolve.error} />}

      {isPending && (
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
      )}
    </div>
  );
}
