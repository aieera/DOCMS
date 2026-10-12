// CB-3 review: the login / MFA / passkey responses carry session_token —
// the same secret as the HttpOnly dms_session cookie. The SPA must not
// retain it anywhere (it used to be kept in JS memory for the /ws auth
// frame, where post-login XSS could lift the whole session). The API
// layer drops it before any caller sees the response.
import { describe, it, expect, beforeEach } from 'vitest'
import { existsSync } from 'node:fs'
import { resolve } from 'node:path'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'
import { login, verifyMFA } from '@/api/auth'
import { verifyEmailOTP, verifySMSOTP, verifyPushChallenge } from '@/api/mfa'
import { useAuthStore } from '@/store/authStore'

const SECRET = 'sess-secret-do-not-keep'
const user = { id: 'u-1', email: 'a@x', role: 'member', tenant_id: 't-1' }

beforeEach(() => {
  useAuthStore.setState({ user: null, tenantId: null, isAuthenticated: false, isHydrating: false, hydrationPromise: null })
  server.use(
    http.post('*/api/v1/auth/login', () => HttpResponse.json({ user, session_token: SECRET, expires_at: 'x' })),
    http.post('*/api/v1/auth/mfa/verify', () => HttpResponse.json({ user, session_token: SECRET })),
    http.post('*/api/v1/auth/mfa/email/verify', () => HttpResponse.json({ user, session_token: SECRET })),
    http.post('*/api/v1/auth/mfa/sms/verify', () => HttpResponse.json({ user, session_token: SECRET })),
    http.post('*/api/v1/auth/mfa/push/verify', () => HttpResponse.json({ user, session_token: SECRET })),
  )
})

describe('session_token is not retained (CB-3)', () => {
  it.each([
    ['login', () => login('a@x', 'pw', 'acme')],
    ['verifyMFA', () => verifyMFA('mfa', '123456')],
    ['verifyEmailOTP', () => verifyEmailOTP('mfa', '123456')],
    ['verifySMSOTP', () => verifySMSOTP('mfa', '123456')],
    ['verifyPushChallenge', () => verifyPushChallenge('mfa', 'c', 'ack')],
  ])('%s drops session_token from the response', async (_name, call) => {
    const data = (await call()) as Record<string, unknown>
    expect(data).not.toHaveProperty('session_token')
    expect(JSON.stringify(data)).not.toContain(SECRET)
    expect(data.user).toEqual(user)
  })

  it('there is no in-memory token holder for /ws any more', () => {
    expect(existsSync(resolve(__dirname, '../../lib/collabToken.ts'))).toBe(false)
  })
})
