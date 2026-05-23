import { describe, it, expect } from 'vitest'
import { parseFieldSyntax } from '@/lib/searchParser'

describe('parseFieldSyntax', () => {
  it('passes through plain text with no field tokens', () => {
    const { free, fields } = parseFieldSyntax('quarterly earnings report')
    expect(free).toBe('quarterly earnings report')
    expect(fields).toEqual({})
  })

  it('extracts status:active and maps to lifecycle_state', () => {
    const { free, fields } = parseFieldSyntax('status:active quarterly report ')
    expect(free).toBe('quarterly report')
    expect(fields.lifecycle_state).toEqual(['active'])
  })

  it('maps status:approved alias to lifecycle_state=active', () => {
    const { free, fields } = parseFieldSyntax('status:approved ')
    expect(free).toBe('')
    expect(fields.lifecycle_state).toEqual(['active'])
  })

  it('maps status:hold alias to lifecycle_state=legal_hold', () => {
    const { fields } = parseFieldSyntax('status:hold ')
    expect(fields.lifecycle_state).toEqual(['legal_hold'])
  })

  it('maps type:pdf to application/pdf MIME type', () => {
    const { fields } = parseFieldSyntax('type:pdf ')
    expect(fields.mime_type).toEqual(['application/pdf'])
  })

  it('maps type:word to full docx MIME type', () => {
    const { fields } = parseFieldSyntax('type:word ')
    expect(fields.mime_type).toEqual(['application/vnd.openxmlformats-officedocument.wordprocessingml.document'])
  })

  it('extracts tag and author in one query', () => {
    const { free, fields } = parseFieldSyntax('tag:contract author:alice annual summary ')
    expect(free).toBe('annual summary')
    expect(fields.tag).toEqual(['contract'])
    expect(fields.author).toEqual(['alice'])
  })

  it('collects multiple values for the same field', () => {
    const { fields } = parseFieldSyntax('tag:contract tag:legal ')
    expect(fields.tag).toEqual(['contract', 'legal'])
  })

  it('does NOT extract the last token when string has no trailing space (in-progress)', () => {
    const { free, fields } = parseFieldSyntax('status:in_re')
    expect(free).toBe('status:in_re')
    expect(fields).toEqual({})
  })

  it('extracts the last token when string ends with a space', () => {
    const { free, fields } = parseFieldSyntax('status:draft ')
    expect(free).toBe('')
    expect(fields.lifecycle_state).toEqual(['draft'])
  })

  it('middle field tokens are always extracted regardless of trailing space', () => {
    const { free, fields } = parseFieldSyntax('status:active report')
    // 'report' is the last token (no trailing space), stays as free text
    // 'status:active' is NOT the last token, so it IS extracted
    expect(free).toBe('report')
    expect(fields.lifecycle_state).toEqual(['active'])
  })

  it('handles unrecognized field names as free text', () => {
    const { free, fields } = parseFieldSyntax('foo:bar baz ')
    expect(free).toBe('foo:bar baz')
    expect(fields).toEqual({})
  })

  it('empty input returns empty free and empty fields', () => {
    const { free, fields } = parseFieldSyntax('')
    expect(free).toBe('')
    expect(fields).toEqual({})
  })
})
