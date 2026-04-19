import { createFileRoute } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'
import { ComplianceDashboard } from '@/components/admin/ComplianceDashboard'

function CompliancePage() {
  return (
    <div>
      <PageHeader title="Compliance" description="Data residency, encryption, and retention overview" />
      <ComplianceDashboard
        docsByState={[
          { name: 'active', count: 1240 }, { name: 'draft', count: 380 },
          { name: 'archived', count: 560 }, { name: 'in_review', count: 95 },
        ]}
        storageByRegion={[
          { name: 'us-east-1', gb: 245 }, { name: 'eu-west-1', gb: 128 }, { name: 'me-south-1', gb: 67 },
        ]}
        encryptionCoverage={94}
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/compliance')({ component: CompliancePage })
