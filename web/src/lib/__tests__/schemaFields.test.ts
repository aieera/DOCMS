import { describe, it, expect } from 'vitest'
import {
  listFields,
  upsertField,
  removeField,
  toggleRequired,
  specFromField,
  fieldFromSpec,
  validateFieldName,
  type SchemaField,
} from '@/lib/schemaFields'

const base = {
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  type: 'object',
  properties: {
    invoice_number: { type: 'string', description: 'e.g. INV-1' },
    amount_usd: { type: 'number', minimum: 0 },
  },
  required: ['invoice_number'],
}

describe('listFields', () => {
  it('lists properties with required flags in declaration order', () => {
    const f = listFields(base)
    expect(f.map((x) => x.name)).toEqual(['invoice_number', 'amount_usd'])
    expect(f[0].required).toBe(true)
    expect(f[1].required).toBe(false)
  })
  it('tolerates a schema with no properties', () => {
    expect(listFields({ type: 'object' })).toEqual([])
    expect(listFields(null)).toEqual([])
  })
})

describe('specFromField / fieldFromSpec', () => {
  it('round-trips a string field with constraints', () => {
    const field: SchemaField = {
      name: 'vendor', type: 'string', description: 'Supplier',
      required: false, minLength: 2, maxLength: 40, pattern: '^[A-Z]',
    }
    const spec = specFromField(field)
    expect(spec).toEqual({ type: 'string', description: 'Supplier', minLength: 2, maxLength: 40, pattern: '^[A-Z]' })
    expect(fieldFromSpec('vendor', spec, false)).toMatchObject(field)
  })
  it('maps the date pseudo-type to string+format', () => {
    expect(specFromField({ name: 'due', type: 'date', required: false })).toEqual({ type: 'string', format: 'date' })
    expect(fieldFromSpec('due', { type: 'string', format: 'date' }, false).type).toBe('date')
  })
  it('emits enum choices and drops empty ones', () => {
    const spec = specFromField({ name: 's', type: 'enum', required: false, enumValues: ['a', '', ' b '] })
    expect(spec).toEqual({ type: 'string', enum: ['a', 'b'] })
    expect(fieldFromSpec('s', spec, false).enumValues).toEqual(['a', 'b'])
  })
  it('omits empty optional keys entirely', () => {
    expect(specFromField({ name: 'x', type: 'number', required: false, description: '  ' })).toEqual({ type: 'number' })
  })
})

describe('upsertField', () => {
  it('appends a new field and records required', () => {
    const next = upsertField(base, { name: 'vendor', type: 'string', required: true })
    expect(Object.keys(next.properties as object)).toEqual(['invoice_number', 'amount_usd', 'vendor'])
    expect(next.required).toEqual(['invoice_number', 'vendor'])
  })
  it('edits in place, preserving field order', () => {
    const next = upsertField(base, { name: 'invoice_number', type: 'string', description: 'new', required: true })
    expect(Object.keys(next.properties as object)).toEqual(['invoice_number', 'amount_usd'])
    expect((next.properties as never)['invoice_number']).toEqual({ type: 'string', description: 'new' })
  })
  it('renames while keeping position and required membership', () => {
    const next = upsertField(base, { name: 'inv_no', type: 'string', required: true }, 'invoice_number')
    expect(Object.keys(next.properties as object)).toEqual(['inv_no', 'amount_usd'])
    expect(next.required).toEqual(['inv_no'])
  })
  it('unrequiring drops the name and removes an empty required array', () => {
    const next = upsertField(base, { name: 'invoice_number', type: 'string', required: false })
    expect(next.required).toBeUndefined()
  })
  it('seeds root keys when starting from an empty schema', () => {
    const next = upsertField({}, { name: 'a', type: 'string', required: false })
    expect(next.type).toBe('object')
    expect(next.$schema).toContain('json-schema.org')
  })
})

describe('removeField', () => {
  it('removes the property and its required entry', () => {
    const next = removeField(base, 'invoice_number')
    expect(Object.keys(next.properties as object)).toEqual(['amount_usd'])
    expect(next.required).toBeUndefined()
  })
  it('is a no-op for an unknown field', () => {
    expect(Object.keys(removeField(base, 'nope').properties as object)).toHaveLength(2)
  })
})

describe('toggleRequired', () => {
  it('flips both directions', () => {
    const on = toggleRequired(base, 'amount_usd')
    expect(on.required).toEqual(['invoice_number', 'amount_usd'])
    expect(toggleRequired(on, 'amount_usd').required).toEqual(['invoice_number'])
  })
})

describe('validateFieldName', () => {
  it('requires a non-empty identifier-ish name', () => {
    expect(validateFieldName('', [])).toMatch(/required/i)
    expect(validateFieldName('has space', [])).toMatch(/letters/i)
    expect(validateFieldName('ok_name1', [])).toBeNull()
  })
  it('rejects duplicates but allows keeping your own name', () => {
    expect(validateFieldName('vendor', ['vendor'])).toMatch(/already/i)
    expect(validateFieldName('vendor', ['vendor'], 'vendor')).toBeNull()
  })
})
