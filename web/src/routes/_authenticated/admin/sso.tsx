import { useState } from 'react'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { ShieldCheck, Trash2, CheckCircle2, AlertTriangle, XCircle, Power, PowerOff } from 'lucide-react'
import {
  createSSOConfig,
  deleteSSOConfig,
  listSSOConfigs,
  updateSSOConfig,
  validateSSOConfig,
  type OIDCConfigBody,
  type RoleRule,
  type RoleMapping,
  type Provider,
  type SAMLConfigBody,
  type SSOConfig,
  type ValidateResult,
} from '@/api/sso'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatRelativeTime } from '@/lib/formatters'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

type Step = 'pick' | 'configure' | 'attrs' | 'validate' | 'done'

export function SsoPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({ queryKey: ['admin', 'sso-configs'], queryFn: listSSOConfigs })

  const [wizardOpen, setWizardOpen] = useState(false)

  const toggleActive = useAppMutation({
    mutationFn: (c: SSOConfig) => updateSSOConfig(c.id, { is_active: !c.is_active }),
    onSuccess: () => {
      toast.success('SSO config updated')
      qc.invalidateQueries({ queryKey: ['admin', 'sso-configs'] })
    },
  })

  const remove = useAppMutation({
    mutationFn: (id: string) => deleteSSOConfig(id),
    onSuccess: () => {
      toast.success('SSO config deleted')
      qc.invalidateQueries({ queryKey: ['admin', 'sso-configs'] })
    },
  })

  return (
    <div>
      <PageHeader
        title="Single Sign-On"
        description="Connect your SAML or OIDC identity provider. Users with matching email domains then bypass password login."
        actions={
          <Button onClick={() => setWizardOpen((w) => !w)}>
            <ShieldCheck className="h-4 w-4" />
            {wizardOpen ? 'Close wizard' : 'New connection'}
          </Button>
        }
      />

      {wizardOpen && <Wizard onDone={() => {
        setWizardOpen(false)
        qc.invalidateQueries({ queryKey: ['admin', 'sso-configs'] })
      }} />}

      {isLoading ? (
        <Skeleton className="h-24" />
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<ShieldCheck className="h-12 w-12" />}
          title="No identity providers configured"
          description="Click New connection to set up SAML or OIDC."
        />
      ) : (
        <ul className="space-y-2">
          {data.map((c) => (
            <li
              key={c.id}
              className="flex items-start justify-between rounded-lg border border-border bg-card p-3"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <ShieldCheck className="h-4 w-4" />
                  <span className="font-medium">{c.display_name}</span>
                  <Badge variant="default">{c.provider.toUpperCase()}</Badge>
                  <Badge variant={c.is_active ? 'active' : 'archived'}>
                    {c.is_active ? 'Active' : 'Paused'}
                  </Badge>
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  Updated {formatRelativeTime(c.updated_at)}
                </div>
                <pre className="mt-2 max-h-32 overflow-auto rounded bg-background p-2 text-xs">
{JSON.stringify(c.config, null, 2)}
                </pre>
              </div>
              <div className="flex shrink-0 gap-2">
                <Button onClick={() => toggleActive.mutate(c)} disabled={toggleActive.isPending}>
                  {c.is_active ? <PowerOff className="h-4 w-4" /> : <Power className="h-4 w-4" />}
                </Button>
                <Button
                  onClick={() => {
                    if (window.confirm(`Delete SSO connection"${c.display_name}"?`)) remove.mutate(c.id)
                  }}
                  disabled={remove.isPending}
                  aria-label={`Delete SSO connection: ${c.display_name}`}
                  title={`Delete SSO connection: ${c.display_name}`}
                >
                  <Trash2 className="h-4 w-4" aria-hidden="true" />
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Wizard
// ---------------------------------------------------------------------------

function Wizard({ onDone }: { onDone: () => void }) {
  const [step, setStep] = useState<Step>('pick')
  const [provider, setProvider] = useState<Provider>('saml')
  const [displayName, setDisplayName] = useState('')

  // SAML state
  const [metaSource, setMetaSource] = useState<'url' | 'xml'>('url')
  const [metaURL, setMetaURL] = useState('')
  const [metaXML, setMetaXML] = useState('')
  const [attrEmail, setAttrEmail] = useState('')
  const [attrName, setAttrName] = useState('')
  const [attrGroups, setAttrGroups] = useState('')

  // OIDC state
  const [issuerURL, setIssuerURL] = useState('')
  const [clientID, setClientID] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [redirectURL, setRedirectURL] = useState('')
  const [scopesText, setScopesText] = useState('openid profile email')

  // Claim → role mapping (shared by SAML + OIDC).
  const [roleRules, setRoleRules] = useState<RoleRule[]>([])
  const [defaultRole, setDefaultRole] = useState('member')

  const [validation, setValidation] = useState<ValidateResult | null>(null)

  const roleMapping = (): RoleMapping => ({
    rules: roleRules.filter((r) => r.claim_value.trim() && r.role),
    default_role: defaultRole,
  })

  const buildConfig = (): SAMLConfigBody | OIDCConfigBody => {
    if (provider === 'saml') {
      const cfg: SAMLConfigBody = {
        attribute_mapping: {
          email: attrEmail || undefined,
          display_name: attrName || undefined,
          groups: attrGroups || undefined,
        },
        role_mapping: roleMapping(),
      }
      if (metaSource === 'url') cfg.idp_metadata_url = metaURL
      else cfg.idp_metadata_xml = metaXML
      return cfg
    }
    const cfg: OIDCConfigBody = {
      issuer_url: issuerURL,
      client_id: clientID,
      client_secret: clientSecret,
      redirect_url: redirectURL,
      scopes: scopesText.split(/\s+/).filter(Boolean),
      role_mapping: roleMapping(),
    }
    return cfg
  }

  const validate = useAppMutation({
    mutationFn: () => validateSSOConfig(provider, buildConfig()),
    onSuccess: (r) => {
      setValidation(r)
      if (r.ok) setStep('validate')
    },
    onError: () => toast.error('Validation failed'),
  })

  const save = useAppMutation({
    mutationFn: () =>
      createSSOConfig({
        provider,
        display_name: displayName,
        config: buildConfig(),
        is_active: true,
      }),
    onSuccess: () => {
      toast.success('SSO connection created')
      setStep('done')
      onDone()
    },
    onError: (err: unknown) => {
      const m =
        typeof err === 'object' && err && 'message' in err
          ? String((err as { message?: string }).message)
          : 'Create failed'
      toast.error(m)
    },
  })

  const steps: { id: Step; label: string }[] = [
    { id: 'pick', label: '1. Provider' },
    { id: 'configure', label: '2. Config' },
    { id: 'attrs', label: '3. Attributes' },
    { id: 'validate', label: '4. Validate' },
  ]

  return (
    <div className="mb-6 rounded-lg border border-border bg-card p-4">
      <div className="mb-4 flex items-center gap-2 text-xs">
        {steps.map((s, i) => (
          <span key={s.id} className="flex items-center gap-2">
            <span
              className={`rounded-full px-2 py-0.5 ${
                step === s.id ? 'bg-primary text-white' : 'bg-background'
              }`}
            >
              {s.label}
            </span>
            {i < steps.length - 1 && <DirectionalIcon name="ArrowRight" className="h-3 w-3 text-muted-foreground" />}
          </span>
        ))}
      </div>

      {step === 'pick' && (
        <div className="space-y-3">
          <Field label="Connection name">
            <input
              className="w-full rounded-md border border-border bg-background px-2 py-1 text-sm"
              placeholder="Corporate Okta"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </Field>
          <Field label="Protocol">
            <div className="flex gap-2">
              {(['saml', 'oidc'] as const).map((p) => (
                <button
                  key={p}
                  onClick={() => setProvider(p)}
                  className={`rounded-md border px-3 py-1 text-sm ${
                    provider === p
                      ? 'border-primary bg-accent'
                      : 'border-border'
                  }`}
                >
                  {p.toUpperCase()}
                </button>
              ))}
            </div>
          </Field>
          <Button
            onClick={() => {
              if (!displayName.trim()) { toast.error('Connection name is required'); return }
              setStep('configure')
            }}
          >
            Continue
          </Button>
        </div>
      )}

      {step === 'configure' && provider === 'saml' && (
        <div className="space-y-3">
          <Field label="Metadata source">
            <div className="flex gap-2">
              {(['url', 'xml'] as const).map((s) => (
                <button
                  key={s}
                  onClick={() => setMetaSource(s)}
                  className={`rounded-md border px-3 py-1 text-sm ${
                    metaSource === s
                      ? 'border-primary bg-accent'
                      : 'border-border'
                  }`}
                >
                  {s === 'url' ? 'Metadata URL' : 'Paste XML'}
                </button>
              ))}
            </div>
          </Field>
          {metaSource === 'url' ? (
            <Field label="IdP metadata URL (https://…)">
              <input
                className="w-full rounded-md border border-border bg-background px-2 py-1 text-sm font-mono"
                placeholder="https://idp.example.com/metadata.xml"
                value={metaURL}
                onChange={(e) => setMetaURL(e.target.value)}
              />
            </Field>
          ) : (
            <Field label="IdP metadata XML">
              <textarea
                className="h-40 w-full rounded-md border border-border bg-background px-2 py-1 font-mono text-xs"
                placeholder="<EntityDescriptor …>"
                value={metaXML}
                onChange={(e) => setMetaXML(e.target.value)}
              />
            </Field>
          )}
          <WizardNav onBack={() => setStep('pick')} onNext={() => setStep('attrs')} />
        </div>
      )}

      {step === 'configure' && provider === 'oidc' && (
        <div className="space-y-3">
          <Field label="Issuer URL (https://…)">
            <input
              className="w-full rounded-md border border-border bg-background px-2 py-1 text-sm font-mono"
              placeholder="https://id.example.com"
              value={issuerURL}
              onChange={(e) => setIssuerURL(e.target.value)}
            />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Client ID">
              <input
                className="w-full rounded-md border border-border bg-background px-2 py-1 text-sm"
                value={clientID}
                onChange={(e) => setClientID(e.target.value)}
              />
            </Field>
            <Field label="Client Secret">
              <input
                type="password"
                className="w-full rounded-md border border-border bg-background px-2 py-1 text-sm"
                value={clientSecret}
                onChange={(e) => setClientSecret(e.target.value)}
              />
            </Field>
          </div>
          <Field label="Redirect URL (registered with the IdP)">
            <input
              className="w-full rounded-md border border-border bg-background px-2 py-1 text-sm font-mono"
              placeholder="https://vaultdms.example.com/api/v1/auth/oidc/<slug>/callback"
              value={redirectURL}
              onChange={(e) => setRedirectURL(e.target.value)}
            />
          </Field>
          <Field label="Scopes (space-separated)">
            <input
              className="w-full rounded-md border border-border bg-background px-2 py-1 text-sm"
              value={scopesText}
              onChange={(e) => setScopesText(e.target.value)}
            />
          </Field>
          <WizardNav onBack={() => setStep('pick')} onNext={() => setStep('attrs')} />
        </div>
      )}

      {step === 'attrs' && provider === 'saml' && (
        <div className="space-y-3">
          <p className="text-xs text-muted-foreground">
            Map SAML assertion attributes (URIs or friendly names) to user fields. Leave blank to
            fall back to the provider's defaults.
          </p>
          <Field label="Email attribute">
            <input
              className="w-full rounded-md border border-border bg-background px-2 py-1 text-sm font-mono"
              placeholder="http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"
              value={attrEmail}
              onChange={(e) => setAttrEmail(e.target.value)}
            />
          </Field>
          <Field label="Display name attribute">
            <input
              className="w-full rounded-md border border-border bg-background px-2 py-1 text-sm font-mono"
              placeholder="http://schemas.microsoft.com/ws/2008/06/identity/claims/displayname"
              value={attrName}
              onChange={(e) => setAttrName(e.target.value)}
            />
          </Field>
          <Field label="Groups attribute">
            <input
              className="w-full rounded-md border border-border bg-background px-2 py-1 text-sm font-mono"
              placeholder="http://schemas.xmlsoap.org/claims/Group"
              value={attrGroups}
              onChange={(e) => setAttrGroups(e.target.value)}
            />
          </Field>
          <RoleMappingEditor rules={roleRules} setRules={setRoleRules} defaultRole={defaultRole} setDefaultRole={setDefaultRole} />
          <WizardNav
            onBack={() => setStep('configure')}
            onNext={() => validate.mutate()}
            nextLabel={validate.isPending ? 'Validating…' : 'Validate'}
            nextDisabled={validate.isPending}
          />
          {validation && !validation.ok && <ValidationBanner result={validation} />}
        </div>
      )}

      {step === 'attrs' && provider === 'oidc' && (
        <div className="space-y-3">
          <p className="text-xs text-muted-foreground">
            OIDC uses standard claims (<code>email</code>, <code>name</code>, <code>groups</code>).
            No attribute mapping is required — click Validate to check the issuer's discovery doc.
          </p>
          <RoleMappingEditor rules={roleRules} setRules={setRoleRules} defaultRole={defaultRole} setDefaultRole={setDefaultRole} />
          <WizardNav
            onBack={() => setStep('configure')}
            onNext={() => validate.mutate()}
            nextLabel={validate.isPending ? 'Validating…' : 'Validate'}
            nextDisabled={validate.isPending}
          />
          {validation && !validation.ok && <ValidationBanner result={validation} />}
        </div>
      )}

      {step === 'validate' && validation && (
        <div className="space-y-3">
          <ValidationBanner result={validation} />
          <WizardNav
            onBack={() => setStep('attrs')}
            onNext={() => save.mutate()}
            nextLabel={save.isPending ? 'Saving…' : 'Create connection'}
            nextDisabled={save.isPending}
          />
        </div>
      )}

      {step === 'done' && (
        <div className="flex items-center gap-2 text-sm text-success dark:text-emerald-400">
          <CheckCircle2 className="h-5 w-5" />
          Connection created. You can pause/delete it from the list below.
        </div>
      )}
    </div>
  )
}

function WizardNav({
  onBack,
  onNext,
  nextLabel = 'Continue',
  nextDisabled = false,
}: {
  onBack: () => void
  onNext: () => void
  nextLabel?: string
  nextDisabled?: boolean
}) {
  return (
    <div className="flex gap-2">
      <Button onClick={onBack}>Back</Button>
      <Button onClick={onNext} disabled={nextDisabled}>
        {nextLabel}
      </Button>
    </div>
  )
}

function ValidationBanner({ result }: { result: ValidateResult }) {
  return (
    <div
      className={`rounded-md border p-3 text-sm ${
        result.ok
          ? 'border-emerald-500 bg-success/10/30'
          : 'border-red-500 bg-destructive/10 dark:bg-red-950/30'
      }`}
    >
      <div className="flex items-center gap-2 font-medium">
        {result.ok ? (
          <CheckCircle2 className="h-4 w-4 text-success" />
        ) : (
          <XCircle className="h-4 w-4 text-destructive" />
        )}
        {result.ok ? 'Validation passed' : 'Validation failed'}
      </div>
      {result.error && <p className="mt-1 text-xs">{result.error}</p>}
      {result.warnings && result.warnings.length > 0 && (
        <ul className="mt-2 space-y-1 text-xs">
          {result.warnings.map((w, i) => (
            <li key={i} className="flex items-center gap-1 text-warning dark:text-warning">
              <AlertTriangle className="h-3 w-3" />
              {w}
            </li>
          ))}
        </ul>
      )}
      {result.details && Object.keys(result.details).length > 0 && (
        <pre className="mt-2 overflow-auto rounded bg-background p-2 text-xs">
{JSON.stringify(result.details, null, 2)}
        </pre>
      )}
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block text-xs">
      <span className="mb-1 block text-muted-foreground">{label}</span>
      {children}
    </label>
  )
}

const ROLE_OPTIONS = ['owner', 'admin', 'member', 'guest']

// RoleMappingEditor: map IdP group/role claim values → SeDoc roles for
// JIT-provisioned users. First matching rule wins; else the default role.
function RoleMappingEditor({ rules, setRules, defaultRole, setDefaultRole }: {
  rules: RoleRule[]
  setRules: (r: RoleRule[]) => void
  defaultRole: string
  setDefaultRole: (r: string) => void
}) {
  const roleSelect = 'rounded-md border border-border bg-background px-2 py-1 text-sm'
  return (
    <div className="space-y-2 rounded-md border border-border p-3">
      <p className="text-xs font-semibold uppercase text-muted-foreground">Claim → role mapping</p>
      <p className="text-xs text-muted-foreground">
        Map a group/role claim value to a SeDoc role for JIT-provisioned users. First match wins; otherwise the default applies.
      </p>
      {rules.map((r, i) => (
        <div key={i} className="flex items-center gap-2" data-testid={`role-rule-${i}`}>
          <input
            className="flex-1 rounded-md border border-border bg-background px-2 py-1 text-sm"
            placeholder="claim value (e.g. Admins)"
            value={r.claim_value}
            onChange={(e) => setRules(rules.map((x, k) => (k === i ? { ...x, claim_value: e.target.value } : x)))}
          />
          <span className="text-muted-foreground">→</span>
          <select className={roleSelect} value={r.role}
            onChange={(e) => setRules(rules.map((x, k) => (k === i ? { ...x, role: e.target.value } : x)))}>
            {ROLE_OPTIONS.map((x) => <option key={x} value={x}>{x}</option>)}
          </select>
          <button type="button" className="text-destructive" onClick={() => setRules(rules.filter((_, k) => k !== i))}>✕</button>
        </div>
      ))}
      <div className="flex items-center gap-2">
        <button type="button" className="text-xs font-medium text-primary" data-testid="add-role-rule"
          onClick={() => setRules([...rules, { claim_value: '', role: 'member' }])}>
          + Add rule
        </button>
        <span className="ms-auto text-xs text-muted-foreground">Default role</span>
        <select className={roleSelect} value={defaultRole} onChange={(e) => setDefaultRole(e.target.value)} data-testid="default-role">
          {ROLE_OPTIONS.map((x) => <option key={x} value={x}>{x}</option>)}
        </select>
      </div>
    </div>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/identity?sub=sso). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/sso')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/identity', search: { sub: 'sso' }, replace: true })
  },
})
