import { useState } from "react";
import { ErrorBanner, Skeleton, StatusBadge } from "../components/ui";
import { useRetrySync, useSyncLog } from "../hooks/queries";

// [F] Sync Dashboard — operational view of the worker pipeline (sync_log + DLQ
// retry). Auto-refreshes (the hook polls).
export function SyncDashboard() {
  const [filter, setFilter] = useState("");
  const q = useSyncLog(filter);
  const retry = useRetrySync();

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">Sync dashboard</h1>
        <select
          aria-label="Filter by status"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          className="rounded border px-2 py-1 text-sm"
        >
          <option value="">all</option>
          <option value="pending">pending</option>
          <option value="done">done</option>
          <option value="failed">failed (DLQ)</option>
        </select>
      </div>
      {retry.error && <ErrorBanner error={retry.error} />}
      {q.isLoading ? (
        <Skeleton rows={8} />
      ) : q.error ? (
        <ErrorBanner error={q.error} />
      ) : (
        <table className="w-full text-sm">
          <thead className="text-left text-xs uppercase text-gray-500">
            <tr>
              <th className="py-1">Kind</th>
              <th>Customer</th>
              <th>Status</th>
              <th>Attempts</th>
              <th>Last error</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {(q.data?.items ?? []).map((r) => (
              <tr key={r.id} className="border-t align-top">
                <td className="py-1.5">{r.kind}</td>
                <td>{r.customer_ref || "—"}</td>
                <td><StatusBadge status={r.status} /></td>
                <td className="text-gray-500">{r.attempts}</td>
                <td className="max-w-xs truncate text-xs text-red-700" title={r.last_error}>
                  {r.last_error}
                  {r.correlation_id && <span className="ml-1 font-mono text-gray-400">({r.correlation_id})</span>}
                </td>
                <td>
                  {r.status === "failed" && (
                    <button
                      onClick={() => retry.mutate(r.id)}
                      disabled={retry.isPending}
                      className="text-xs text-brand underline disabled:opacity-50"
                    >
                      Retry
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
