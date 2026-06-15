import { useEffect, useState, type ReactNode } from "react";
import { Empty, ErrorBanner, Skeleton, StatusBadge } from "../components/ui";
import { toast } from "../components/toast";
import {
  useBackfillDetail,
  useBackfillRuns,
  useRetrySync,
  useStartBackfill,
  useSyncLog,
  useSyncMetrics,
} from "../hooks/queries";
import type { BackfillRun } from "../lib/types";

// [F] Sync Dashboard — operational view of the worker pipeline: live throughput
// + backlog + DLQ + rate-limiter metrics, a backfill control panel, and the
// sync_log / DLQ table. Everything auto-refreshes (the hooks poll).
export function SyncDashboard() {
  return (
    <div className="space-y-6">
      <h1 className="text-lg font-semibold">Sync dashboard</h1>
      <MetricsPanel />
      <BackfillPanel />
      <SyncLogTable />
    </div>
  );
}

// ---- metrics ---------------------------------------------------------------

function MetricsPanel() {
  const m = useSyncMetrics();
  const [samples, setSamples] = useState<number[]>([]);
  useEffect(() => {
    if (m.data) setSamples((s) => [...s.slice(-29), m.data!.events_per_min]);
  }, [m.dataUpdatedAt]); // one sample per successful poll

  if (m.isLoading && !m.data) return <Skeleton rows={2} />;
  if (m.error) return <ErrorBanner error={m.error} />;
  const d = m.data;
  if (!d) return null;
  const lim = d.limiter;

  return (
    <section className="space-y-3">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <Stat label="Events / min" value={d.events_per_min} hint={`${d.processed_total} total`} />
        <Stat label="Backlog" value={d.backlog} hint="pending" />
        <Stat label="DLQ" value={d.dlq} tone={d.dlq > 0 ? "warn" : undefined} hint="failed" />
        <Stat label="SeDoc 429s" value={d.http_429} tone={d.http_429 > 0 ? "warn" : undefined} hint="throttle breaches" />
        <Stat
          label="Limiter tokens"
          value={lim ? lim.tokens_available.toFixed(1) : "—"}
          hint={lim ? `${lim.limit_per_sec}/s · burst ${lim.burst}` : ""}
        />
        <Stat
          label="Throttle waits"
          value={lim ? lim.wait_count : "—"}
          hint={lim ? `${(lim.wait_total_ms / 1000).toFixed(1)}s waited` : ""}
        />
      </div>
      <div className="rounded border p-3">
        <div className="mb-2 text-xs text-gray-500">Throughput (events/min, last {samples.length} samples)</div>
        <Sparkline values={samples} />
      </div>
    </section>
  );
}

function Stat({
  label,
  value,
  hint,
  tone,
}: {
  label: string;
  value: ReactNode;
  hint?: string;
  tone?: "warn";
}) {
  return (
    <div className="rounded border p-3">
      <div className="text-xs text-gray-500">{label}</div>
      <div className={`text-xl font-semibold ${tone === "warn" ? "text-amber-700" : ""}`}>{value}</div>
      {hint && <div className="text-[11px] text-gray-400">{hint}</div>}
    </div>
  );
}

function Sparkline({ values }: { values: number[] }) {
  if (values.length === 0) return <div className="h-12 text-xs text-gray-400">collecting…</div>;
  const max = Math.max(1, ...values);
  return (
    <div className="flex h-12 items-end gap-0.5" role="img" aria-label="throughput sparkline">
      {values.map((v, i) => (
        <div
          key={i}
          className="w-1.5 rounded-t bg-brand"
          style={{ height: `${Math.max(2, (v / max) * 100)}%` }}
          title={`${v}/min`}
        />
      ))}
    </div>
  );
}

// ---- backfill --------------------------------------------------------------

function BackfillPanel() {
  const runs = useBackfillRuns();
  const start = useStartBackfill();
  const [refs, setRefs] = useState("");
  const [openId, setOpenId] = useState<string>();

  function onStart() {
    const list = refs.split(",").map((s) => s.trim()).filter(Boolean);
    start.mutate(list, {
      onSuccess: (r) => {
        toast(`Backfill started (${r.run_id.slice(0, 8)}…)`, "success");
        setRefs("");
      },
    });
  }

  const items = runs.data?.items ?? [];
  return (
    <section className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="font-semibold">Backfill</h2>
        <div className="flex items-center gap-2">
          <input
            aria-label="Customer refs to scope (optional, comma-separated)"
            placeholder="customer refs (optional)"
            value={refs}
            onChange={(e) => setRefs(e.target.value)}
            className="w-64 rounded border px-2 py-1 text-xs"
          />
          <button
            onClick={onStart}
            disabled={start.isPending}
            className="rounded bg-brand px-3 py-1 text-xs text-white disabled:opacity-40"
          >
            {start.isPending ? "Starting…" : "Start backfill"}
          </button>
        </div>
      </div>
      {start.error && <ErrorBanner error={start.error} />}
      {runs.isLoading ? (
        <Skeleton rows={3} />
      ) : runs.error ? (
        <ErrorBanner error={runs.error} />
      ) : items.length === 0 ? (
        <Empty>No backfill runs yet. Start one to onboard existing ERP data.</Empty>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-left text-xs uppercase text-gray-500">
            <tr>
              <th className="py-1">Run</th>
              <th>Status</th>
              <th>Progress</th>
              <th>Failed</th>
              <th>Started</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {items.map((run) => (
              <BackfillRow
                key={run.id}
                run={run}
                open={openId === run.id}
                onToggle={() => setOpenId(openId === run.id ? undefined : run.id)}
              />
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function BackfillRow({ run, open, onToggle }: { run: BackfillRun; open: boolean; onToggle: () => void }) {
  const pct = run.total > 0 ? Math.round((run.processed / run.total) * 100) : 0;
  return (
    <>
      <tr className="border-t align-top">
        <td className="py-1.5 font-mono text-xs">{run.id.slice(0, 8)}…</td>
        <td><StatusBadge status={run.status} /></td>
        <td>
          <div className="flex items-center gap-2">
            <div className="h-2 w-28 overflow-hidden rounded bg-gray-100">
              <div className="h-full bg-brand" style={{ width: `${pct}%` }} />
            </div>
            <span className="text-xs text-gray-500">
              {run.processed}/{run.total}
            </span>
          </div>
        </td>
        <td className={run.failed > 0 ? "text-amber-700" : "text-gray-500"}>{run.failed}</td>
        <td className="text-xs text-gray-500">
          {run.started_at ? new Date(run.started_at).toLocaleTimeString() : "—"}
        </td>
        <td>
          {run.failed > 0 && (
            <button onClick={onToggle} className="text-xs text-brand underline">
              {open ? "hide" : "failures"}
            </button>
          )}
        </td>
      </tr>
      {open && (
        <tr>
          <td colSpan={6} className="bg-gray-50 px-3 py-2">
            <BackfillFailures runId={run.id} />
          </td>
        </tr>
      )}
    </>
  );
}

function BackfillFailures({ runId }: { runId: string }) {
  const detail = useBackfillDetail(runId);
  if (detail.isLoading) return <Skeleton rows={2} />;
  if (detail.error) return <ErrorBanner error={detail.error} />;
  const fails = detail.data?.failures ?? [];
  if (fails.length === 0) return <div className="text-xs text-gray-500">No per-item failures.</div>;
  return (
    <ul className="space-y-1 text-xs">
      {fails.map((f, i) => (
        <li key={i}>
          <span className="font-medium">{f.customer_ref || "—"}</span>{" "}
          <span className="font-mono">{f.item}</span>
          <span className="text-red-700"> — {f.error}</span>
          {f.correlation_id && <span className="ml-1 font-mono text-gray-400">({f.correlation_id})</span>}
        </li>
      ))}
    </ul>
  );
}

// ---- sync log --------------------------------------------------------------

function SyncLogTable() {
  const [filter, setFilter] = useState("");
  const q = useSyncLog(filter);
  const retry = useRetrySync();

  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between">
        <h2 className="font-semibold">Sync log</h2>
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
    </section>
  );
}
