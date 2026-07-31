import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { UsersPage } from './users'
import { GroupsPage } from './groups'
import { PermissionsPage } from './permissions'
import { SsoPage } from './sso'
import { LDAPAdminPage } from './tenant/identity/ldap'
import { ScimPage } from './scim'

// Merge #1 — Identity & Access. Two outer tabs from the plan:
//   People & Roles → users / groups / permission matrix
//   Authentication → SSO + LDAP/AD + SCIM provisioning
// SCIM (IdP user/group provisioning + deprovision) folds in here as an
// authentication concern alongside SSO/LDAP. Sub-tab state is URL-driven
// via `?group=` and `?sub=` so deep links (incl. the old /admin/scim, which
// still resolves standalone) keep working.
type Group = 'people' | 'auth'
type Sub =
  | 'users'
  | 'groups'
  | 'permissions'
  | 'sso'
  | 'ldap'
  | 'scim'

interface S { group?: Group; sub?: Sub }

const PEOPLE_SUBS: readonly Sub[] = ['users', 'groups', 'permissions']
const AUTH_SUBS: readonly Sub[] = ['sso', 'ldap', 'scim']

function IdentityPage() {
  const navigate = useNavigate()
  const { group, sub } = Route.useSearch()
  const activeGroup: Group = group ?? 'people'
  const activeSub: Sub =
    sub && (activeGroup === 'people' ? PEOPLE_SUBS : AUTH_SUBS).includes(sub)
      ? sub
      : activeGroup === 'people'
        ? 'users'
        : 'sso'

  return (
    <div className="space-y-4">
      <Tabs
        value={activeGroup}
        onValueChange={(v) =>
          navigate({
            to: '/admin/identity',
            search: { group: v as Group },
          })
        }
      >
        <TabsList>
          <TabsTrigger value="people">People &amp; Roles</TabsTrigger>
          <TabsTrigger value="auth">Authentication</TabsTrigger>
        </TabsList>

        <TabsContent value="people" className="mt-4">
          <Tabs
            value={activeSub}
            onValueChange={(v) =>
              navigate({
                to: '/admin/identity',
                search: { group: 'people', sub: v as Sub },
              })
            }
          >
            <TabsList>
              <TabsTrigger value="users">Users</TabsTrigger>
              <TabsTrigger value="groups">Groups</TabsTrigger>
              <TabsTrigger value="permissions">Permission matrix</TabsTrigger>
            </TabsList>
            <TabsContent value="users" className="mt-4"><UsersPage /></TabsContent>
            <TabsContent value="groups" className="mt-4"><GroupsPage /></TabsContent>
            <TabsContent value="permissions" className="mt-4"><PermissionsPage /></TabsContent>
          </Tabs>
        </TabsContent>

        <TabsContent value="auth" className="mt-4">
          <Tabs
            value={activeSub}
            onValueChange={(v) =>
              navigate({
                to: '/admin/identity',
                search: { group: 'auth', sub: v as Sub },
              })
            }
          >
            <TabsList>
              <TabsTrigger value="sso">SSO (SAML / OIDC)</TabsTrigger>
              <TabsTrigger value="ldap">LDAP / AD</TabsTrigger>
              <TabsTrigger value="scim">SCIM provisioning</TabsTrigger>
            </TabsList>
            <TabsContent value="sso" className="mt-4"><SsoPage /></TabsContent>
            <TabsContent value="ldap" className="mt-4"><LDAPAdminPage /></TabsContent>
            <TabsContent value="scim" className="mt-4"><ScimPage /></TabsContent>
          </Tabs>
        </TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/identity')({
  component: IdentityPage,
  validateSearch: (raw: Record<string, unknown>): S => {
    const g = raw.group
    const s = raw.sub
    const out: S = {}
    if (g === 'people' || g === 'auth') out.group = g
    if (
      s === 'users' ||
      s === 'groups' ||
      s === 'permissions' ||
      s === 'sso' ||
      s === 'ldap' ||
      s === 'scim'
    ) out.sub = s
    return out
  },
})
