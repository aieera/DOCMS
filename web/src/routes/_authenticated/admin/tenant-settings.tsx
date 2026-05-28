import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { SettingsPage } from './settings'
import { UploadPolicyPage } from './tenant/upload-policy'

// Merge #7 — Tenant feature flags + per-tenant upload policy.
//
// Note on the license overlap from the source prompt: the License
// page (kept standalone at /admin/tenant/license) holds the signed
// JWT feature entitlements; the Tenant feature-flags panel here only
// flips runtime visibility within whatever the license already grants.
// We surface that distinction visually with the tab labels below.
type Tab = 'flags' | 'upload'
interface S { tab?: Tab }

function TenantSettingsPage() {
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'flags'
  return (
    <Tabs
      value={active}
      onValueChange={(v) => navigate({ to: '/admin/tenant-settings', search: { tab: v as Tab } })}
    >
      <TabsList>
        <TabsTrigger value="flags">Feature flags</TabsTrigger>
        <TabsTrigger value="upload">Upload policy</TabsTrigger>
      </TabsList>
      <TabsContent value="flags" className="mt-4"><SettingsPage /></TabsContent>
      <TabsContent value="upload" className="mt-4"><UploadPolicyPage /></TabsContent>
    </Tabs>
  )
}

export const Route = createFileRoute('/_authenticated/admin/tenant-settings')({
  component: TenantSettingsPage,
  validateSearch: (raw: Record<string, unknown>): S => {
    const t = raw.tab
    return t === 'flags' || t === 'upload' ? { tab: t } : {}
  },
})
