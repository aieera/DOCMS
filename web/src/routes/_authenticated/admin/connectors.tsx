import { createFileRoute } from '@tanstack/react-router'
import { Plug } from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'

function ConnectorsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Connectors"
        description="Sync documents to and from M365, Salesforce, Google Drive, and other source systems."
      />
      <EmptyState
        icon={<Plug className="h-6 w-6" />}
        title="No connectors installed"
        description="Install a connector to push or pull documents between VaultDMS and an external system. Each connector is tenant-scoped and audited."
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/connectors')({ component: ConnectorsPage })
