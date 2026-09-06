// QA SD-08: backend status codes reached users verbatim, sometimes
// doubled — grpc-gateway wraps pkg/errors strings as
// "InvalidArgument: INVALID_ARGUMENT: must be > 0" (CamelCase gateway
// code + SNAKE domain code), which the SNAKE-only prefix matcher passed
// through untouched. Raw lower-case backend fragments ("user with this
// email already exists") also shipped as-is.
import { describe, it, expect } from 'vitest'
import { humanizeStatusMessage } from '@/api/client'

describe('humanizeStatusMessage (SD-08)', () => {
  it('keeps translating the SNAKE domain codes', () => {
    expect(humanizeStatusMessage('INVALID_ARGUMENT: required'))
      .toBe('A required value was missing.')
  })

  it('recognises grpc-gateway CamelCase code prefixes', () => {
    expect(humanizeStatusMessage('InvalidArgument: length must be 1..255'))
      .toBe('That request was rejected as invalid. (length must be 1..255)')
  })

  it('strips a doubled gateway+domain prefix down to one sentence', () => {
    expect(humanizeStatusMessage('InvalidArgument: INVALID_ARGUMENT: must be > 0'))
      .toBe('That request was rejected as invalid. (must be > 0)')
  })

  it('sentence-cases raw lower-case backend fragments', () => {
    expect(humanizeStatusMessage('user with this email already exists'))
      .toBe('User with this email already exists')
  })

  it('leaves already-human strings alone', () => {
    expect(humanizeStatusMessage('Executable file types are not permitted'))
      .toBe('Executable file types are not permitted')
  })
})
