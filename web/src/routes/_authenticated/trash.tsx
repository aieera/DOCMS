import { createFileRoute } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Trash2 } from 'lucide-react'

function TrashPage() {
  return (
    <div>
      <PageHeader title="Trash" description="Deleted items — restore or permanently delete" />
      <EmptyState icon={<Trash2 className="h-12 w-12" />} title="Trash is empty" />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/trash')({ component: TrashPage })
