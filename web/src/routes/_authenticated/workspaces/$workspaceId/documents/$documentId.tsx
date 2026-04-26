import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useDocument } from '@/hooks/useDocuments'
import { Badge } from '@/components/ui/Badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { Spinner } from '@/components/ui/Spinner'
import { Button } from '@/components/ui/Button'
import { RegionPinBadge } from '@/components/shared/RegionPinBadge'
import { LegalHoldBadge } from '@/components/shared/LegalHoldBadge'
import { CollabPresence } from '@/components/shared/CollabPresence'
import { SignaturePanel } from '@/features/signatures/SignaturePanel'
import { RedactDialog } from '@/features/redaction/RedactDialog'
import { getVersions } from '@/api/documents'
import { useAuthStore } from '@/store/authStore'
import { formatFileSize, formatDateTime } from '@/lib/formatters'
import { Download, Share, History, MessageSquare, ScissorsLineDashed } from 'lucide-react'

function DocumentDetailPage() {
  const { documentId } = Route.useParams()
  const { data: doc, isLoading } = useDocument(documentId)
  // Fetch versions to know the current version_id for signature
  // requests. Backend resolves "current" if we send the latest id;
  // an empty array means the doc has no content yet (newly created).
  const { data: versions } = useQuery({
    queryKey: ['document', documentId, 'versions'],
    queryFn: () => getVersions(documentId),
    enabled: !!doc,
  })
  const currentVersionID = versions?.[0]?.id ?? ''
  // Role gate mirrors the backend's requireRole at
  // services/document/internal/handler/redaction_handler.go:79.
  // The frontend User.role union doesn't enumerate compliance_officer
  // (that's a tenant-administered role flag, not the canonical app
  // role) so we string-match to keep TS happy.
  const userRole = useAuthStore((s) => s.user?.role) as string | undefined
  const canRedact = !!userRole && ['compliance_officer', 'admin', 'owner'].includes(userRole)
  const [redactOpen, setRedactOpen] = useState(false)
  // Held = either the boolean flag OR hold_count > 0. Backend keeps
  // both in sync; frontend treats either truthy as "held". Mutating
  // actions (redact, share, request-signature) need to be disabled
  // because the backend will refuse with 423 anyway — surfacing it
  // pre-flight saves the user a confused error toast.
  const held = !!doc?.under_legal_hold || (doc?.hold_count ?? 0) > 0
  const heldTip = held ? 'Document is on legal hold and cannot be modified.' : undefined

  if (isLoading) return <div className="flex justify-center py-16"><Spinner className="h-8 w-8" /></div>
  if (!doc) return <div className="py-16 text-center text-sm text-[var(--color-text-secondary)]">Document not found</div>

  return (
    <div className="flex gap-6">
      <div className="flex-1">
        <div className="mb-4 flex items-center gap-3">
          <FileIcon mime={doc.mime_type} className="h-8 w-8" />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <h1 className="truncate text-xl font-bold">{doc.title}</h1>
              {held && <LegalHoldBadge count={doc.hold_count} />}
              <CollabPresence documentID={documentId} showConnectionState />
            </div>
            <p className="text-sm text-[var(--color-text-secondary)]">{doc.created_by_name} · {formatDateTime(doc.created_at)}</p>
          </div>
        </div>
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 text-center text-sm text-[var(--color-text-secondary)]">
          Document viewer placeholder — PDF/image/video viewer renders here
        </div>
      </div>
      <aside className="w-80 shrink-0 space-y-4">
        <div className="flex gap-2">
          <Button variant="outline" size="sm" title="Download is allowed even when held — content can be read, just not modified.">
            <Download className="h-4 w-4" /> Download
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={held}
            title={heldTip}
            data-testid="document-share"
          >
            <Share className="h-4 w-4" /> Share
          </Button>
        </div>
        <SignaturePanel documentID={doc.id} versionID={currentVersionID} disabled={held} disabledReason={heldTip} />
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 space-y-3">
          <h3 className="text-sm font-semibold">Details</h3>
          <div className="space-y-2 text-sm">
            <Row label="Status"><Badge variant={doc.lifecycle_state}>{doc.lifecycle_state}</Badge></Row>
            {doc.region_pin && <Row label="Region"><RegionPinBadge region={doc.region_pin} size="sm" /></Row>}
            <Row label="Type">{doc.document_class || 'Unclassified'}</Row>
            <Row label="Size">{formatFileSize(doc.size_bytes)}</Row>
            <Row label="Versions">{doc.version_count}</Row>
            <Row label="MIME">{doc.mime_type}</Row>
          </div>
          {doc.tags.length > 0 && (
            <div>
              <p className="mb-1 text-xs font-medium text-[var(--color-text-secondary)]">Tags</p>
              <div className="flex flex-wrap gap-1">
                {doc.tags.map((t) => <Badge key={t}>{t}</Badge>)}
              </div>
            </div>
          )}
        </div>
        <div className="flex flex-col gap-2">
          <Button variant="ghost" size="sm" className="justify-start"><History className="h-4 w-4" /> Version History</Button>
          <Button variant="ghost" size="sm" className="justify-start"><MessageSquare className="h-4 w-4" /> Comments</Button>
          {canRedact && (
            <Button
              variant="ghost"
              size="sm"
              className="justify-start text-red-700 hover:text-red-800 dark:text-red-300"
              data-testid="document-redact"
              disabled={held}
              title={heldTip}
              onClick={() => setRedactOpen(true)}
            >
              <ScissorsLineDashed className="h-4 w-4" /> Redact
            </Button>
          )}
        </div>
      </aside>
      <RedactDialog
        open={redactOpen}
        onOpenChange={setRedactOpen}
        documentID={doc.id}
        versionID={currentVersionID}
      />
    </div>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex justify-between">
      <span className="text-[var(--color-text-secondary)]">{label}</span>
      <span>{children}</span>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/workspaces/$workspaceId/documents/$documentId')({ component: DocumentDetailPage })
