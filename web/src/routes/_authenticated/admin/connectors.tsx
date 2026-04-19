import { createFileRoute } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'
import { Plug } from 'lucide-react'

function ConnectorsPage() {
  return (
    <div>
      <PageHeader title="Connectors" description="Third-party integrations" />
      <div className="flex flex-col items-center justify-center rounded-lg border border-dashed p-12 text-center">
        <Plug className="h-10 w-10 text-muted-foreground" />
        <h3 className="mt-4 text-lg font-medium">No connectors installed</h3>
        <p className="mt-1 text-sm text-muted-foreground">Connect third-party services to extend your document management capabilities.</p>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/connectors')({ component: ConnectorsPage })
