import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'
import { useAuthStore } from '@/store/authStore'
import { startQES, sendViaESign, recordSignature, signInPerson } from '@/api/signatures'

// S-14 / DEC-17 (SIG1 contract): QES start and e-sign send take ids
// only — the server loads the request in a tenant transaction and reads
// the bytes from its own (document_id, version_id). The client must not
// download, base64 or hash the document, and must not ship signer
// identity. S-13: the per-signer record call carries ?token=.
describe('signatures API — ids-only contract', () => {
  beforeEach(() => {
    useAuthStore.setState({
      user: { id: 'u-1', email: 'a@x', role: 'member', tenant_id: 't-1' } as never,
      tenantId: 't-1', isAuthenticated: true, isHydrating: false, hydrationPromise: null,
    })
  })

  it('startQES posts only request/signer ids, provider and optional reason', async () => {
    let body: Record<string, unknown> = {}
    server.use(
      http.post('*/api/v1/signatures/qes/start', async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ session_id: 's', redirect_url: 'https://tsp', provider: 'mock', expires_at: '' }, { status: 201 })
      }),
    )
    await startQES({ request_id: 'r-1', signer_id: 'sg-1', provider: 'mock', reason: 'Signed via SeDoc' })
    expect(Object.keys(body).sort()).toEqual(['provider', 'reason', 'request_id', 'signer_id'])
    expect(body).not.toHaveProperty('document_bytes_b64')
    expect(body).not.toHaveProperty('signer_email')
    expect(body).not.toHaveProperty('signer_name')
  })

  it('sendViaESign posts the request id and embedded signer ids, no bytes or recipients', async () => {
    let body: Record<string, unknown> = {}
    server.use(
      http.post('*/api/v1/signatures/esign/send', async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ envelope_id: 'e', status: 'sent' }, { status: 201 })
      }),
    )
    await sendViaESign({
      request_id: 'r-1',
      provider: 'docusign',
      document_name: 'document.pdf',
      embedded_signer_ids: ['sg-2'],
      subject: 's',
      message: 'm',
      return_url: 'http://localhost/sign/done',
    })
    expect(body.request_id).toBe('r-1')
    expect(body.embedded_signer_ids).toEqual(['sg-2'])
    expect(body).not.toHaveProperty('document_bytes_b64')
    expect(body).not.toHaveProperty('recipients')
  })

  it('recordSignature forwards the signer token and sends no client hash', async () => {
    let url = ''
    let body: Record<string, unknown> = {}
    server.use(
      http.post('*/api/v1/signatures/requests/:id/sign/:signerId', async ({ request }) => {
        url = request.url
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ status: 'signed' })
      }),
    )
    await recordSignature({
      request_id: 'r-1', signer_id: 'sg-1', token: 'abc123', svg_path: 'M0 0', device_kind: 'desktop',
    })
    const u = new URL(url)
    expect(u.pathname).toBe('/api/v1/signatures/requests/r-1/sign/sg-1')
    expect(u.searchParams.get('token')).toBe('abc123')
    expect(body).not.toHaveProperty('doc_hash_sha256')
  })

  it('signInPerson sends no client hash', async () => {
    let body: Record<string, unknown> = {}
    server.use(
      http.post('*/api/v1/signatures/requests/:id/in-person/sign', async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ status: 'signed' })
      }),
    )
    await signInPerson({ request_id: 'r-1', signer_id: 'sg-1', svg_path: 'M0 0', device_kind: 'tablet' })
    expect(body).not.toHaveProperty('doc_hash_sha256')
    expect(body.signer_id).toBe('sg-1')
  })
})
