import { useState } from "react";
import { useApiClient } from "../lib/apiContext";
import { FolderTree } from "../components/FolderTree";
import { Empty, ErrorBanner, Skeleton, StatusBadge, SyncIndicator, bytesHuman } from "../components/ui";
import {
  useCustomerTree,
  useDocumentDetail,
  useFolderDocuments,
  useRestoreVersion,
  useUpload,
} from "../hooks/queries";
import type { DocumentRow } from "../lib/types";

// [D] Customer File Explorer — the core feature, scoped to one customer. The
// customer is supplied by the host (the ERP customer record, or the :ref route in
// standalone dev) via the customerRef prop — no routing assumptions here. Tree →
// doc list → detail/versions, plus drag-drop upload into Attachments.
export function FileExplorer({ customerRef: ref }: { customerRef: string }) {
  const tree = useCustomerTree(ref);
  const [folderId, setFolderId] = useState<string | undefined>();
  const [docId, setDocId] = useState<string | undefined>();

  if (tree.isLoading) return <Skeleton rows={6} />;
  if (tree.error) return <ErrorBanner error={tree.error} />;
  if (!tree.data) return <Empty>Customer not found.</Empty>;

  return (
    <div className="grid grid-cols-12 gap-4">
      <aside className="col-span-3">
        <h2 className="mb-2 text-sm font-semibold text-gray-700">{tree.data.name}</h2>
        <FolderTree folders={tree.data.folders} selectedId={folderId} onSelect={(id) => { setFolderId(id); setDocId(undefined); }} />
        <UploadZone customerRef={ref} />
      </aside>

      <section className="col-span-5" aria-label="Documents">
        {!folderId ? (
          <Empty>Select a folder to see its documents.</Empty>
        ) : (
          <DocumentList customerRef={ref} folderId={folderId} onOpen={setDocId} selectedId={docId} />
        )}
      </section>

      <section className="col-span-4" aria-label="Document detail">
        {docId ? <DocumentDetailPanel docId={docId} /> : <Empty>Open a document to preview it.</Empty>}
      </section>
    </div>
  );
}

function DocumentList({
  customerRef,
  folderId,
  onOpen,
  selectedId,
}: {
  customerRef: string;
  folderId: string;
  onOpen: (id: string) => void;
  selectedId?: string;
}) {
  const q = useFolderDocuments(customerRef, folderId);
  if (q.isLoading) return <Skeleton rows={5} />;
  if (q.error) return <ErrorBanner error={q.error} />;
  const docs = q.data?.documents ?? [];
  if (docs.length === 0) return <Empty>No documents in this folder yet.</Empty>;
  return (
    <table className="w-full text-sm">
      <thead className="text-left text-xs uppercase text-gray-500">
        <tr>
          <th className="py-1">Name</th>
          <th>Type</th>
          <th>Status</th>
          <th>Ver</th>
          <th>Sync</th>
        </tr>
      </thead>
      <tbody>
        {docs.map((d: DocumentRow) => (
          <tr
            key={d.id}
            onClick={() => onOpen(d.id)}
            className={`cursor-pointer border-t hover:bg-gray-50 ${selectedId === d.id ? "bg-brand/5" : ""}`}
          >
            <td className="py-1.5">{d.title}</td>
            <td>{d.documentClass || "—"}</td>
            <td><StatusBadge status={d.lifecycleState} /></td>
            <td className="text-gray-500">{d.currentVersionId ? "current" : "—"}</td>
            <td><SyncIndicator state="synced" /></td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function DocumentDetailPanel({ docId }: { docId: string }) {
  const api = useApiClient();
  const q = useDocumentDetail(docId);
  const restore = useRestoreVersion(docId);
  const [downloading, setDownloading] = useState(false);
  if (q.isLoading) return <Skeleton rows={4} />;
  if (q.error) return <ErrorBanner error={q.error} />;
  if (!q.data) return null;
  const { document: doc, versions } = q.data;

  async function onDownload() {
    setDownloading(true);
    try {
      await api.download(doc.id, doc.title || doc.id);
    } finally {
      setDownloading(false);
    }
  }

  return (
    <div className="rounded border p-3">
      <div className="mb-2 flex items-center justify-between">
        <h3 className="font-semibold">{doc.title}</h3>
        <StatusBadge status={doc.lifecycleState} />
      </div>
      <dl className="mb-3 grid grid-cols-2 gap-1 text-xs text-gray-600">
        <dt>Type</dt><dd>{doc.documentClass || "—"}</dd>
        <dt>External id</dt><dd className="font-mono">{doc.externalId || "—"}</dd>
        <dt>Size</dt><dd>{bytesHuman(doc.totalSizeBytes)}</dd>
      </dl>
      <button
        onClick={onDownload}
        disabled={downloading}
        className="mb-3 rounded bg-brand px-3 py-1.5 text-sm text-white disabled:opacity-50"
      >
        {downloading ? "Downloading…" : "Download"}
      </button>

      <h4 className="mb-1 text-xs font-semibold uppercase text-gray-500">Version history</h4>
      {restore.error && <ErrorBanner error={restore.error} />}
      <ul className="space-y-1 text-sm">
        {versions.map((v) => (
          <li key={v.id} className="flex items-center justify-between border-t py-1">
            <span>
              v{v.versionNumber}
              {v.id === doc.currentVersionId && <span className="ml-1 text-xs text-green-700">(current)</span>}
              <span className="ml-2 text-xs text-gray-400">{v.createdByName} · {bytesHuman(v.sizeBytes)}</span>
            </span>
            {v.id !== doc.currentVersionId && (
              <button
                onClick={() => restore.mutate(v.id)}
                disabled={restore.isPending}
                className="text-xs text-brand underline disabled:opacity-50"
              >
                Restore
              </button>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

function UploadZone({ customerRef }: { customerRef: string }) {
  const upload = useUpload(customerRef);
  const [pct, setPct] = useState(0);
  const [over, setOver] = useState(false);
  const [outcome, setOutcome] = useState<string | null>(null);

  function send(file: File) {
    setOutcome(null);
    setPct(0);
    upload.mutate(
      { file, onProgress: setPct },
      {
        onSuccess: (r) => setOutcome(`Filed — ingestion ${r.status} (${r.ingestion_item_id.slice(0, 8)}…)`),
      },
    );
  }

  return (
    <div
      onDragOver={(e) => { e.preventDefault(); setOver(true); }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        setOver(false);
        if (e.dataTransfer.files[0]) send(e.dataTransfer.files[0]);
      }}
      className={`mt-4 rounded border-2 border-dashed p-4 text-center text-xs ${over ? "border-brand bg-brand/5" : "border-gray-300"}`}
    >
      <p className="mb-2 text-gray-600">Drop a file to add to Attachments</p>
      <label className="cursor-pointer text-brand underline">
        browse
        <input
          type="file"
          className="hidden"
          onChange={(e) => e.target.files?.[0] && send(e.target.files[0])}
        />
      </label>
      {upload.isPending && (
        <div className="mt-2 h-1.5 overflow-hidden rounded bg-gray-200" role="progressbar" aria-valuenow={pct}>
          <div className="h-full bg-brand transition-all" style={{ width: `${pct}%` }} />
        </div>
      )}
      {upload.error && <ErrorBanner error={upload.error} />}
      {outcome && <p className="mt-2 text-green-700">{outcome}</p>}
    </div>
  );
}
