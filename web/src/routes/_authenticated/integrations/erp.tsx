import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { FolderTree, FileText, Download, Inbox, Layers } from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/EmptyState'
import { Spinner } from '@/components/ui/Spinner'
import { Button } from '@/components/ui/shadcn/button'
import { cn } from '@/lib/cn'
import {
  listErpCustomers,
  getErpCustomerTree,
  getErpFolderDocuments,
  getErpDocument,
  erpDocumentDownloadURL,
  type ErpDocumentDetail,
} from '@/api/erpIntegration'

function bytesHuman(n: number): string {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1)
  return `${(n / Math.pow(1024, i)).toFixed(i ? 1 : 0)} ${u[i]}`
}

function StatusBadge({ state }: { state: string }) {
  const tone =
    state === 'active'
      ? 'bg-success/15 text-foreground'
      : state === 'in_review' || state === 'draft'
        ? 'bg-warning/15 text-warning-strong'
        : 'bg-muted text-muted-foreground'
  return (
    <span className={cn('inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium capitalize', tone)}>
      {state.replace(/_/g, ' ') || '—'}
    </span>
  )
}

function DetailRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-3">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="truncate text-end text-foreground">{value}</dd>
    </div>
  )
}

function DocumentDetail({ data }: { data: ErpDocumentDetail }) {
  const d = data.document
  return (
    <div className="space-y-4">
      <div>
        <h3 className="break-words font-semibold text-foreground">{d.title}</h3>
        <div className="mt-1.5 flex flex-wrap items-center gap-2">
          <StatusBadge state={d.lifecycleState} />
          {d.documentClass && (
            <span className="text-xs capitalize text-muted-foreground">{d.documentClass}</span>
          )}
        </div>
      </div>
      <dl className="space-y-1.5 text-sm">
        <DetailRow label="External ID" value={d.externalId || '—'} />
        <DetailRow label="Type" value={d.mimeType || '—'} />
        <DetailRow label="Size" value={bytesHuman(d.totalSizeBytes)} />
        <DetailRow
          label="Updated"
          value={d.updatedAt ? new Date(d.updatedAt).toLocaleString() : '—'}
        />
      </dl>
      <a href={erpDocumentDownloadURL(d.id)} download className="block">
        <Button className="w-full gap-2">
          <Download className="h-4 w-4" /> Download
        </Button>
      </a>
      <div>
        <div className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Versions ({data.versions.length})
        </div>
        <ul className="space-y-1">
          {data.versions.map((v) => (
            <li
              key={v.id}
              className="flex items-center justify-between rounded-lg border border-border/60 px-3 py-1.5 text-sm"
            >
              <span>
                v{v.versionNumber}
                {v.label ? ` · ${v.label}` : ''}
              </span>
              <span className="text-xs text-muted-foreground">{bytesHuman(v.sizeBytes)}</span>
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

function ErpExplorerPage() {
  const [ref, setRef] = useState('')
  const [folderId, setFolderId] = useState('')
  const [docId, setDocId] = useState('')

  const customersQ = useQuery({
    queryKey: ['erp-customers'],
    queryFn: listErpCustomers,
    staleTime: 60_000,
  })
  const treeQ = useQuery({
    queryKey: ['erp-tree', ref],
    queryFn: () => getErpCustomerTree(ref),
    enabled: !!ref,
  })
  const docsQ = useQuery({
    queryKey: ['erp-docs', ref, folderId],
    queryFn: () => getErpFolderDocuments(ref, folderId),
    enabled: !!ref && !!folderId,
  })
  const detailQ = useQuery({
    queryKey: ['erp-doc', docId],
    queryFn: () => getErpDocument(docId),
    enabled: !!docId,
  })

  const folders = treeQ.data?.folders ?? []

  // Reset downstream selection when the customer changes.
  useEffect(() => {
    setFolderId('')
    setDocId('')
  }, [ref])
  // Auto-select the first folder once the tree loads (or after a customer swap).
  useEffect(() => {
    if (folders.length && !folders.some((f) => f.id === folderId)) {
      setFolderId(folders[0].id)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [treeQ.data])
  // Clear the open document when the folder changes.
  useEffect(() => {
    setDocId('')
  }, [folderId])

  const customers = customersQ.data ?? []

  return (
    <div>
      <PageHeader
        title="ERP Documents"
        description="Customer files synced from the ERP. Pick a customer to browse their folder tree — quotes, POs, SOs, DOs, invoices and attachments."
        actions={
          <select
            value={ref}
            onChange={(e) => setRef(e.target.value)}
            className="h-9 min-w-[15rem] rounded-lg border border-input bg-muted px-3 text-sm shadow-neu-inset"
            aria-label="Select customer"
          >
            <option value="">
              {customersQ.isLoading ? 'Loading customers…' : 'Select a customer…'}
            </option>
            {customers.map((c) => (
              <option key={c.customer_ref} value={c.customer_ref}>
                {(c.name || c.customer_ref) + ' (' + c.customer_ref + ')'}
              </option>
            ))}
          </select>
        }
      />

      {!ref ? (
        <Card>
          <EmptyState
            icon={<Inbox className="h-6 w-6" />}
            title={customers.length ? 'Select a customer' : 'No customers provisioned yet'}
            description={
              customers.length
                ? 'Choose a customer above to browse their ERP-synced documents.'
                : 'A customer’s folder tree is created automatically the first time that customer is created or synced in the ERP.'
            }
          />
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
          {/* Folder tree */}
          <Card className="p-2 lg:col-span-3">
            <div className="px-2 py-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Folders
            </div>
            {treeQ.isLoading ? (
              <div className="flex justify-center py-8">
                <Spinner />
              </div>
            ) : treeQ.isError ? (
              <p className="px-3 py-6 text-sm text-destructive">
                Couldn’t load this customer’s folders.
              </p>
            ) : (
              <ul className="space-y-0.5">
                {folders.map((f) => (
                  <li key={f.id}>
                    <button
                      onClick={() => setFolderId(f.id)}
                      className={cn(
                        'flex w-full items-center gap-2 rounded-lg px-3 py-2 text-start text-sm transition-colors',
                        f.id === folderId ? 'bg-primary/10 text-foreground' : 'hover:bg-muted',
                      )}
                    >
                      <FolderTree className="h-4 w-4 shrink-0 opacity-70" />
                      <span className="truncate">{f.name}</span>
                      <span className="ms-auto text-xs text-muted-foreground">{f.documentCount}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          {/* Document list */}
          <Card className="overflow-hidden lg:col-span-5">
            {!folderId ? (
              <EmptyState icon={<Layers className="h-6 w-6" />} title="Pick a folder" />
            ) : docsQ.isLoading ? (
              <div className="flex justify-center py-12">
                <Spinner />
              </div>
            ) : docsQ.isError ? (
              <p className="px-4 py-12 text-center text-sm text-destructive">
                Couldn’t load documents.
              </p>
            ) : (docsQ.data?.length ?? 0) === 0 ? (
              <EmptyState icon={<FileText className="h-6 w-6" />} title="No documents in this folder" />
            ) : (
              <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border text-start text-xs text-muted-foreground">
                    <th className="px-4 py-2 font-medium">Name</th>
                    <th className="px-4 py-2 font-medium">Type</th>
                    <th className="px-4 py-2 font-medium">Status</th>
                  </tr>
                </thead>
                <tbody>
                  {(docsQ.data ?? []).map((d) => (
                    <tr
                      key={d.id}
                      onClick={() => setDocId(d.id)}
                      className={cn(
                        'cursor-pointer border-b border-border/60 hover:bg-muted/50',
                        d.id === docId && 'bg-primary/5',
                      )}
                    >
                      <td className="px-4 py-2.5">
                        <div className="flex items-center gap-2">
                          <FileText className="h-4 w-4 shrink-0 text-muted-foreground" />
                          <span className="truncate">{d.title}</span>
                        </div>
                      </td>
                      <td className="px-4 py-2.5 capitalize text-muted-foreground">
                        {d.documentClass || '—'}
                      </td>
                      <td className="px-4 py-2.5">
                        <StatusBadge state={d.lifecycleState} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              </div>
            )}
          </Card>

          {/* Detail panel */}
          <Card className="p-4 lg:col-span-4">
            {!docId ? (
              <EmptyState
                icon={<FileText className="h-6 w-6" />}
                title="No document selected"
                description="Select a document to see its details and versions."
              />
            ) : detailQ.isLoading ? (
              <div className="flex justify-center py-12">
                <Spinner />
              </div>
            ) : detailQ.isError || !detailQ.data ? (
              <p className="text-sm text-destructive">Couldn’t load this document.</p>
            ) : (
              <DocumentDetail data={detailQ.data} />
            )}
          </Card>
        </div>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/integrations/erp')({
  component: ErpExplorerPage,
})
