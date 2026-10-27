// A `?tenant=` search param is only a convenience pre-fill for the sign-in
// form's tenant field. Keep it when it looks like a tenant slug (letters,
// digits, hyphens; at most 63 chars) and drop anything else, so a crafted
// link cannot stuff arbitrary text into the form.
const TENANT_SLUG = /^[a-z0-9][a-z0-9-]{0,62}$/i

export function tenantHint(value: unknown): string | undefined {
  return typeof value === 'string' && TENANT_SLUG.test(value) ? value : undefined
}
