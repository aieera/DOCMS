import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Save, AlertTriangle } from 'lucide-react'

import { getTenantSettings, updateTenantSettings } from '@/api/admin'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { Card } from '@/components/ui/card'

// Tenant-wide feature flags (ADR 0019). Backed by GET / PUT
// /api/v1/admin/settings on the billing service. Owner-only — the
// billing handler enforces it, and the admin landing only links here
// for owner/admin roles.
//
// The legacy version of this file rendered the personal MFA + session
// page (/settings/security duplicates), which was the wrong surface
// for /admin/settings. This route now matches its title and the link
// from the admin landing.

interface FeatureFlags {
  ai_enabled: boolean
  advanced_workflow: boolean
  sso_enabled: boolean
  e_signatures: boolean
  custom_branding: boolean
  api_access: boolean
  data_rooms: boolean
}

const FLAG_LABELS: Record<keyof FeatureFlags, { title: string; help: string }> = {
  ai_enabled:        { title: 'AI features',          help: 'Doc Q&A, semantic search, classification, NER.' },
  advanced_workflow: { title: 'Advanced workflows',   help: 'Approval chains, parallel routing, conditional branches.' },
  sso_enabled:       { title: 'SSO (SAML / OIDC)',    help: 'External identity providers for tenant sign-in.' },
  e_signatures:      { title: 'E-signatures',         help: 'In-app signing + DocuSign / Adobe Sign / QES connectors.' },
  custom_branding:   { title: 'Custom branding',      help: 'Tenant logo + accent color on signing + share-link pages.' },
  api_access:        { title: 'Public API',           help: 'External API keys + scoped REST access.' },
  data_rooms:        { title: 'Data rooms',           help: 'Time-limited M&A / due-diligence collaboration spaces.' },
}

export function SettingsPage() {
  const qc = useQueryClient()
  const { data, isLoading, isError, error, refetch } = useQuery<FeatureFlags>({
    queryKey: ['admin', 'tenant-settings'],
    queryFn: () => getTenantSettings() as Promise<FeatureFlags>,
  })
  const [draft, setDraft] = useState<FeatureFlags | null>(null)
  useEffect(() => { if (data) setDraft({ ...data }) }, [data])

  const save = useMutation({
    mutationFn: () => updateTenantSettings(draft as unknown as Record<string, unknown>),
    onSuccess: (out) => {
      toast.success('Settings saved')
      qc.setQueryData(['admin', 'tenant-settings'], out)
      setDraft({ ...(out as FeatureFlags) })
    },
    onError: (e: Error) => toast.error(e.message || 'Save failed'),
  })

  const dirty = !!draft && !!data && Object.keys(FLAG_LABELS).some((k) =>
    draft[k as keyof FeatureFlags] !== data[k as keyof FeatureFlags],
  )

  return (
    <div className="space-y-6">
      <PageHeader
        title="Tenant settings"
        description="Feature flags for this tenant. Owner-only; changes apply immediately to every workspace."
        actions={
          <Button
            onClick={() => save.mutate()}
            disabled={!dirty}
            loading={save.isPending}
            data-testid="save-tenant-settings"
          >
            <Save className="me-1 h-4 w-4" /> Save
          </Button>
        }
      />

      {isLoading && <Skeleton className="h-64" />}

      {isError && (
        <Card className="flex items-start gap-2 border-destructive/40 bg-destructive/5 p-4 text-sm" data-testid="tenant-settings-error">
          <AlertTriangle className="mt-0.5 h-4 w-4 text-destructive" />
          <div className="flex-1">
            <p className="font-medium">Could not load tenant settings</p>
            <p className="text-muted-foreground">
              {error instanceof Error ? error.message : 'Server error — please retry.'}
            </p>
          </div>
          <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
        </Card>
      )}

      {draft && (
        <ul className="space-y-2" data-testid="feature-flags-list">
          {(Object.keys(FLAG_LABELS) as (keyof FeatureFlags)[]).map((key) => (
            <li key={key}>
              <Card className="flex flex-wrap items-center justify-between gap-3 p-4">
                <div className="min-w-0 flex-1">
                  <p className="font-medium">{FLAG_LABELS[key].title}</p>
                  <p className="text-xs text-muted-foreground">{FLAG_LABELS[key].help}</p>
                </div>
                <label className="inline-flex cursor-pointer items-center gap-2 text-xs">
                  <input
                    type="checkbox"
                    checked={draft[key]}
                    onChange={(e) => setDraft({ ...draft, [key]: e.target.checked })}
                    className="h-4 w-4 rounded border-border"
                    data-testid={`flag-${key}`}
                  />
                  {draft[key] ? 'Enabled' : 'Disabled'}
                </label>
              </Card>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/settings')({ component: SettingsPage })
