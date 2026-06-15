import type { ReactNode } from "react";
import { ApiError } from "../lib/types";

// StatusBadge — color + TEXT (never color alone, for a11y/contrast).
const statusStyles: Record<string, string> = {
  draft: "bg-gray-100 text-gray-700",
  in_review: "bg-amber-100 text-amber-800",
  active: "bg-green-100 text-green-800",
  superseded: "bg-gray-100 text-gray-500",
  archived: "bg-gray-100 text-gray-500",
  // ingestion / review
  received: "bg-blue-100 text-blue-800",
  processed: "bg-blue-100 text-blue-800",
  needs_review: "bg-amber-100 text-amber-800",
  committed: "bg-green-100 text-green-800",
  pending: "bg-amber-100 text-amber-800",
  resolved: "bg-green-100 text-green-800",
  done: "bg-green-100 text-green-800",
  failed: "bg-red-100 text-red-800",
  rejected: "bg-red-100 text-red-800",
  // backfill run states
  running: "bg-blue-100 text-blue-800",
  completed: "bg-green-100 text-green-800",
  canceled: "bg-gray-100 text-gray-500",
};

export function StatusBadge({ status }: { status: string }) {
  const cls = statusStyles[status] ?? "bg-gray-100 text-gray-700";
  return (
    <span className={`inline-block rounded px-2 py-0.5 text-xs font-medium ${cls}`}>
      {status.replace(/_/g, " ")}
    </span>
  );
}

// SyncIndicator — synced / pending / needs-review dot+label.
export function SyncIndicator({ state }: { state: "synced" | "pending" | "needs_review" }) {
  const map = {
    synced: { c: "bg-green-500", t: "Synced" },
    pending: { c: "bg-amber-500", t: "Pending" },
    needs_review: { c: "bg-orange-500", t: "Needs review" },
  }[state];
  return (
    <span className="inline-flex items-center gap-1 text-xs text-gray-600">
      <span className={`h-2 w-2 rounded-full ${map.c}`} aria-hidden /> {map.t}
    </span>
  );
}

// ErrorBanner — human message + copyable correlation_id for support.
export function ErrorBanner({ error }: { error: unknown }) {
  if (!error) return null;
  const msg = error instanceof Error ? error.message : String(error);
  const corr = error instanceof ApiError ? error.correlationId : "";
  return (
    <div role="alert" className="rounded border border-red-200 bg-red-50 p-3 text-sm text-red-800">
      <div>{msg}</div>
      {corr && (
        <button
          className="mt-1 font-mono text-xs underline"
          onClick={() => navigator.clipboard?.writeText(corr)}
          title="Copy correlation id for support"
        >
          correlation_id: {corr} (copy)
        </button>
      )}
    </div>
  );
}

// Skeleton — loading placeholder (not a spinner-on-blank).
export function Skeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="h-6 animate-pulse rounded bg-gray-100" />
      ))}
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="rounded border border-dashed p-6 text-center text-sm text-gray-500">{children}</div>;
}

export function bytesHuman(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}
