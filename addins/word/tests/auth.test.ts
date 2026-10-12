// W-10: when the signed-in Microsoft account maps to SeDoc users in more
// than one tenant, /auth/m365/exchange answers
// 409 {error, candidates:[{tenant_id, tenant_slug, tenant_name, user_id}]}.
// The add-in dumped that JSON into an error string. getSession now raises
// TenantChoiceRequired with the candidates; after chooseTenant(id) the
// exchange is retried with {tenant_id}.
import { getSession, chooseTenant, clearSession, TenantChoiceRequired } from '../src/auth'

const candidates = [
  { tenant_id: 't-1', tenant_slug: 'acme', tenant_name: 'Acme', user_id: 'u-1' },
  { tenant_id: 't-2', tenant_slug: 'globex', tenant_name: 'Globex', user_id: 'u-2' },
]

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

beforeEach(() => {
  clearSession()
  chooseTenant(null)
  ;(globalThis as Record<string, unknown>).OfficeRuntime = {
    auth: { getAccessToken: vi.fn(async () => 'entra-token') },
  }
  globalThis.fetch = vi.fn() as unknown as typeof fetch
})

describe('M365 exchange tenant picker (W-10)', () => {
  it('a 409 raises TenantChoiceRequired with the candidates', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(json({ error: 'multiple tenants', candidates }, 409))
    const err = await getSession().catch((e) => e)
    expect(err).toBeInstanceOf(TenantChoiceRequired)
    expect((err as TenantChoiceRequired).candidates).toEqual(candidates)
    expect(String((err as Error).message)).not.toContain('{')
  })

  it('after choosing a tenant the exchange carries tenant_id', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(json({ vdms_session_token: 's', user_id: 'u-2', tenant_id: 't-2' }))
    chooseTenant('t-2')
    const s = await getSession()
    expect(s).toEqual({ token: 's', userID: 'u-2' })
    const body = JSON.parse(String(vi.mocked(fetch).mock.calls[0][1]?.body))
    expect(body).toEqual({ ms_access_token: 'entra-token', tenant_id: 't-2' })
  })

  it('other failures stay plain errors', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response('nope', { status: 403 }))
    const err = await getSession().catch((e) => e)
    expect(err).not.toBeInstanceOf(TenantChoiceRequired)
    expect(String(err)).toMatch(/403/)
  })
})
