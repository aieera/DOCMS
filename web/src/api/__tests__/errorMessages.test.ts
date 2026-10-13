// Backend status strings must not reach users verbatim (BUG-31).
//
// pkg/errors ToGRPCError sends validation failures as
// status.Error(codes.InvalidArgument, e.Error()), and Error() formats
// as "<CODE>: <message>" — so the grpc-gateway body is
// {"code":3,"message":"INVALID_ARGUMENT: required"} and that whole
// string used to land in a toast. The direct HTTP handlers emit the
// same vocabulary through the {type, message} HTTPError envelope.
//
// The mapping is deliberately keyed on that shared vocabulary only. An
// unrecognized code must pass through untouched rather than be mangled
// into a wrong sentence — these tests pin both directions.

import { describe, it, expect } from 'vitest'
import { humanizeStatusMessage, readErrorMessage } from '@/api/client'

describe('humanizeStatusMessage', () => {
  it('rewrites the exact string users reported seeing', () => {
    const out = humanizeStatusMessage('INVALID_ARGUMENT: required')
    expect(out).not.toMatch(/INVALID_ARGUMENT/)
    expect(out).toBe('A required value was missing.')
  })

  it('rewrites canonical gRPC code names too', () => {
    expect(humanizeStatusMessage('PERMISSION_DENIED: workspace')).toMatch(/permission/i)
    expect(humanizeStatusMessage('NOT_FOUND: resource not found')).toMatch(/couldn't find/i)
    expect(humanizeStatusMessage('UNAVAILABLE: connection refused')).toMatch(/temporarily unavailable/i)
  })

  it('keeps the backend detail when it carries more than the code does', () => {
    const out = humanizeStatusMessage('LEGAL_HOLD: document is under legal hold until 2027')
    expect(out).toMatch(/legal hold/i)
    expect(out).toMatch(/2027/)
    expect(out).not.toMatch(/LEGAL_HOLD/)
  })

  it('passes through anything that is not a known status prefix', () => {
    // No invented sentences for vocabulary we don't own.
    expect(humanizeStatusMessage('Could not reach the printer')).toBe('Could not reach the printer')
    expect(humanizeStatusMessage('SOME_UNKNOWN_CODE: whatever')).toBe('SOME_UNKNOWN_CODE: whatever')
    // QA SD-08: raw lower-case fragments get sentence-cased so they
    // don't read as debug spew ("user with this email already exists").
    expect(humanizeStatusMessage('name already taken')).toBe('Name already taken')
  })
})

describe('readErrorMessage', () => {
  it('humanizes the grpc-gateway body (message carries the code)', () => {
    const err = {
      response: { status: 400, data: { code: 3, message: 'INVALID_ARGUMENT: required' } },
    }
    expect(readErrorMessage(err)).toBe('A required value was missing.')
  })

  it('rejoins the {type, message} HTTPError envelope so the code is visible', () => {
    // Reading `message` alone surfaced the bare fragment "not a uuid".
    const err = {
      response: { status: 400, data: { type: 'INVALID_ARGUMENT', message: 'not a uuid', field: 'id' } },
    }
    expect(readErrorMessage(err)).toBe("That identifier isn't valid.")
  })

  it('sentence-cases plain lower-case backend messages (SD-08)', () => {
    const err = { response: { status: 409, data: { error: 'name already taken' } } }
    expect(readErrorMessage(err)).toBe('Name already taken')
  })

  it('still returns null when there is no parsable envelope', () => {
    expect(readErrorMessage(new Error('boom'))).toBeNull()
    expect(readErrorMessage(undefined)).toBeNull()
  })
})
