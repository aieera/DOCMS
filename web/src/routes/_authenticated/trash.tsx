import { createFileRoute } from '@tanstack/react-router'
import { Trash2 } from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { ComingSoon } from '@/components/shared/ComingSoon'

// H-2 / L-1: the previous route rendered <EmptyState title="Trash is
// empty"/> regardless of whether anything was deleted, which is a
// lie — the trash feature isn't built. Three backend endpoints are
// missing (tenant-wide list of soft-deleted documents, restore, and
// the explicit decision NOT to ship a user-driven purge); see the
// audit follow-up note in the matching commit message for the full
// surface. Until those land the route surfaces a ComingSoon panel
// so the sidebar link stops misleading users.
function TrashPage() {
  return (
    <div>
      <PageHeader title="Trash" description="Deleted items — restore or permanently delete" />
      <ComingSoon
        icon={<Trash2 className="h-6 w-6" />}
        title="Nothing here yet"
        description="Documents you delete will land here, ready to restore. Permanent deletion stays with the retention pipeline — you'll never need a purge button."
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/trash')({ component: TrashPage })
