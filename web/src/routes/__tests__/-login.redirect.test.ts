import { describe, it, expect } from 'vitest'
import { Route } from '../login'

// L-12: the login route validates ?redirect= to a relative path only.
describe('login ?redirect= (L-12)', () => {
  const validate = (Route.options as unknown as {
    validateSearch: (s: Record<string, unknown>) => { redirect?: string }
  }).validateSearch

  it('keeps a relative redirect', () => {
    expect(validate({ redirect: '/protected/c-1?lt=x' })).toEqual({ redirect: '/protected/c-1?lt=x' })
  })

  it('drops an off-site redirect', () => {
    expect(validate({ redirect: 'https://evil.example' })).toEqual({ redirect: undefined })
    expect(validate({ redirect: '//evil.example' })).toEqual({ redirect: undefined })
  })
})
