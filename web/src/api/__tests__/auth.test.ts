import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'
import { login, logout, getCurrentUser, register } from '@/api/auth'

describe('api/auth', () => {
  it('login() posts credentials + tenant_slug and returns the session payload', async () => {
    const data = await login('admin@acme.local', 'pw', 'acme')
    expect(data.user.email).toBe('admin@acme.local')
    expect(data.user.tenant_id).toBeTruthy()
  })

  it('login() forwards tenant_slug in the request body', async () => {
    let captured: unknown = null
    server.use(
      http.post('*/api/v1/auth/login', async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({
          user: { id: 'u', email: 'x@x.x', tenant_id: 't', display_name: '', role: 'owner', status: 'active', mfa_enabled: false, created_at: '' },
          tenant_id: 't',
        })
      }),
    )
    await login('x@x.x', 'pw', 'my-tenant')
    expect(captured).toMatchObject({ email: 'x@x.x', password: 'pw', tenant_slug: 'my-tenant' })
  })

  it('login() rejects on 401 so callers can toast an error', async () => {
    server.use(
      http.post('*/api/v1/auth/login', () =>
        HttpResponse.json({ error: 'invalid credentials' }, { status: 401 }),
      ),
    )
    await expect(login('a@a.com', 'wrong', 'acme')).rejects.toThrow()
  })

  it('logout() issues POST /auth/logout', async () => {
    let called = false
    server.use(
      http.post('*/api/v1/auth/logout', () => {
        called = true
        return HttpResponse.json({}, { status: 204 })
      }),
    )
    await logout()
    expect(called).toBe(true)
  })

  it('getCurrentUser() returns the /me payload', async () => {
    const me = await getCurrentUser()
    expect(me.email).toBe('admin@acme.local')
  })

  it('register() forwards tenant_slug and display_name', async () => {
    let captured: unknown = null
    server.use(
      http.post('*/api/v1/auth/register', async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({ user_id: 'u', email: 'new@x.x', display_name: 'New', tenant_id: 't' }, { status: 201 })
      }),
    )
    await register('new@x.x', 'pw1234567890', 'New Person', 'acme')
    expect(captured).toMatchObject({
      email: 'new@x.x',
      password: 'pw1234567890',
      display_name: 'New Person',
      tenant_slug: 'acme',
    })
  })
})
