import type { Document } from '@/types/api'
import { Activity } from 'lucide-react'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/shadcn/sheet'
import { Badge } from '@/components/ui/shadcn/badge'
import { Avatar } from '@/components/ui/shadcn/avatar'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { formatFileSize, formatDateTime, lifecycleStateLabel } from '@/lib/formatters'
import { ComingSoon } from '@/components/shared/ComingSoon'
import { VersionHistory } from './VersionHistory'
import { TagEditor } from './TagEditor'

interface Props { doc: Document; open: boolean; onClose: () => void }

export function MetadataPanel({ doc, open, onClose }: Props) {
  return (
    <Sheet open={open} onOpenChange={onClose}>
      <SheetContent>
        <SheetHeader>
          <SheetTitle>{doc.title}</SheetTitle>
        </SheetHeader>
        <Tabs defaultValue="info" className="mt-4">
          <TabsList>
            <TabsTrigger value="info">Info</TabsTrigger>
            <TabsTrigger value="versions">Versions</TabsTrigger>
            <TabsTrigger value="activity">Activity</TabsTrigger>
          </TabsList>
          <TabsContent value="info">
            <div className="space-y-4">
              <div className="space-y-2 text-sm">
                <Row label="Status"><Badge variant={doc.lifecycle_state}>{lifecycleStateLabel(doc.lifecycle_state)}</Badge></Row>
                <Row label="Class">{doc.document_class || '—'}</Row>
                <Row label="Size">{formatFileSize(doc.size_bytes)}</Row>
                <Row label="MIME">{doc.mime_type}</Row>
                <Row label="Versions">{doc.version_count}</Row>
                <Row label="Created">{formatDateTime(doc.created_at)}</Row>
                {doc.updated_at && <Row label="Updated">{formatDateTime(doc.updated_at)}</Row>}
                <Row label="Owner"><div className="flex items-center gap-1.5"><Avatar name={doc.created_by_name} size="sm" />{doc.created_by_name}</div></Row>
              </div>
              <div>
                <p className="mb-1.5 text-xs font-medium text-muted-foreground">Tags</p>
                <TagEditor documentId={doc.id} tags={doc.tags} />
              </div>
            </div>
          </TabsContent>
          <TabsContent value="versions"><VersionHistory documentId={doc.id} /></TabsContent>
          <TabsContent value="activity">
            {/* Wave 5 pattern 2: was a bare "Activity log coming soon"
                <p>, now an honest ComingSoon panel. Copy stays
                deliberately generic — no path, no endpoint name —
                because document viewers may not have admin access
                to wherever an admin-side activity surface ends up
                living, and asserting a route here would either
                404 for them or look like the feature ships today. */}
            <ComingSoon
              icon={<Activity className="h-6 w-6" />}
              title="Activity timeline isn't ready yet"
              description="A per-document activity timeline is coming to this tab."
            />
          </TabsContent>
        </Tabs>
      </SheetContent>
    </Sheet>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="flex items-center justify-between"><span className="text-muted-foreground">{label}</span><span>{children}</span></div>
}
