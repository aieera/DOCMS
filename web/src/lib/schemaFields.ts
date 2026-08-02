// Pure operations backing the Metadata schema page's visual field
// builder. The page keeps the JSON text as its single source of truth;
// the builder parses it, applies one of these, and re-serialises. No
// React here, so the editing rules are unit-testable on their own.

/** UI-level field types. `date`/`datetime` are string+format sugar, and
 *  `enum` is string+enum sugar — JSON Schema has no such types. */
export type FieldType =
  | 'string' | 'number' | 'integer' | 'boolean'
  | 'date' | 'datetime' | 'enum' | 'array'

export interface SchemaField {
  name: string
  /** Human label (JSON Schema `title`). `description` is helper text —
   *  using it as the label leaked placeholders like "e.g. INV-2026-0042"
   *  into the document rail. */
  label?: string
  type: FieldType
  description?: string
  required: boolean
  // string
  minLength?: number
  maxLength?: number
  pattern?: string
  // number / integer
  minimum?: number
  maximum?: number
  // enum
  enumValues?: string[]
  // array
  itemType?: 'string' | 'number' | 'integer' | 'boolean'
}

export const FIELD_TYPES: { value: FieldType; label: string; hint: string }[] = [
  { value: 'string',   label: 'Text',           hint: 'Free text' },
  { value: 'number',   label: 'Number',         hint: 'Decimals allowed' },
  { value: 'integer',  label: 'Whole number',   hint: 'No decimals' },
  { value: 'boolean',  label: 'Yes / No',       hint: 'True or false' },
  { value: 'date',     label: 'Date',           hint: 'YYYY-MM-DD' },
  { value: 'datetime', label: 'Date & time',    hint: 'ISO 8601 timestamp' },
  { value: 'enum',     label: 'Choice list',    hint: 'One of a fixed set' },
  { value: 'array',    label: 'List',           hint: 'Multiple values' },
]

type Spec = Record<string, unknown>
type Schema = Record<string, unknown>

const ROOT_DEFAULTS = {
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  type: 'object',
}

function propsOf(schema: Schema | null): Record<string, Spec> {
  if (!schema || typeof schema !== 'object') return {}
  const p = (schema as Schema).properties
  return p && typeof p === 'object' && !Array.isArray(p) ? (p as Record<string, Spec>) : {}
}

function requiredOf(schema: Schema | null): string[] {
  const r = schema?.required
  return Array.isArray(r) ? (r as string[]).filter((x) => typeof x === 'string') : []
}

/** Field rows for the builder, in schema declaration order. */
export function listFields(schema: Schema | null): SchemaField[] {
  const req = new Set(requiredOf(schema))
  return Object.entries(propsOf(schema)).map(([name, spec]) =>
    fieldFromSpec(name, spec, req.has(name)),
  )
}

/** JSON-Schema property spec for one UI field. Empty values are omitted
 *  so the emitted schema stays minimal and diff-friendly. */
export function specFromField(field: SchemaField): Spec {
  const spec: Spec = {}
  switch (field.type) {
    case 'date':      spec.type = 'string'; spec.format = 'date'; break
    case 'datetime':  spec.type = 'string'; spec.format = 'date-time'; break
    case 'enum':      spec.type = 'string'; break
    case 'array':     spec.type = 'array'; break
    default:          spec.type = field.type
  }
  const label = field.label?.trim()
  if (label) spec.title = label
  const desc = field.description?.trim()
  if (desc) spec.description = desc

  if (field.type === 'enum') {
    const vals = (field.enumValues ?? []).map((v) => v.trim()).filter(Boolean)
    if (vals.length) spec.enum = vals
  }
  if (field.type === 'array' && field.itemType) {
    spec.items = { type: field.itemType }
  }
  if (field.type === 'string') {
    if (isNum(field.minLength)) spec.minLength = field.minLength
    if (isNum(field.maxLength)) spec.maxLength = field.maxLength
    const pat = field.pattern?.trim()
    if (pat) spec.pattern = pat
  }
  if (field.type === 'number' || field.type === 'integer') {
    if (isNum(field.minimum)) spec.minimum = field.minimum
    if (isNum(field.maximum)) spec.maximum = field.maximum
  }
  return spec
}

/** Inverse of specFromField — best-effort read of a hand-written spec. */
export function fieldFromSpec(name: string, spec: Spec, required: boolean): SchemaField {
  const t = typeof spec.type === 'string' ? spec.type : 'string'
  const fmt = typeof spec.format === 'string' ? spec.format : undefined
  let type: FieldType = 'string'
  if (Array.isArray(spec.enum)) type = 'enum'
  else if (t === 'string' && fmt === 'date') type = 'date'
  else if (t === 'string' && fmt === 'date-time') type = 'datetime'
  else if (t === 'number' || t === 'integer' || t === 'boolean' || t === 'array' || t === 'string') {
    type = t as FieldType
  }
  const items = spec.items as Spec | undefined
  return {
    name,
    type,
    required,
    label: typeof spec.title === 'string' ? spec.title : undefined,
    description: typeof spec.description === 'string' ? spec.description : undefined,
    minLength: num(spec.minLength),
    maxLength: num(spec.maxLength),
    pattern: typeof spec.pattern === 'string' ? spec.pattern : undefined,
    minimum: num(spec.minimum),
    maximum: num(spec.maximum),
    enumValues: Array.isArray(spec.enum) ? (spec.enum as unknown[]).map(String) : undefined,
    itemType: items && typeof items.type === 'string'
      ? (items.type as SchemaField['itemType'])
      : undefined,
  }
}

/** Add a field, or edit one in place. Pass `originalName` when renaming
 *  so the field keeps its position and required membership. */
export function upsertField(schema: Schema | null, field: SchemaField, originalName?: string): Schema {
  const next: Schema = { ...ROOT_DEFAULTS, ...(schema ?? {}) }
  const prev = propsOf(schema)
  const from = originalName ?? field.name
  const spec = specFromField(field)

  // Rebuild the map so an edit (and rename) holds its slot rather than
  // jumping to the end.
  const props: Record<string, Spec> = {}
  let placed = false
  for (const [key, value] of Object.entries(prev)) {
    if (key === from) { props[field.name] = spec; placed = true } else { props[key] = value }
  }
  if (!placed) props[field.name] = spec
  next.properties = props

  const req = requiredOf(schema).filter((n) => n !== from && n !== field.name)
  if (field.required) req.push(field.name)
  if (req.length) next.required = req
  else delete next.required
  return next
}

export function removeField(schema: Schema | null, name: string): Schema {
  const next: Schema = { ...ROOT_DEFAULTS, ...(schema ?? {}) }
  const props = { ...propsOf(schema) }
  delete props[name]
  next.properties = props
  const req = requiredOf(schema).filter((n) => n !== name)
  if (req.length) next.required = req
  else delete next.required
  return next
}

export function toggleRequired(schema: Schema | null, name: string): Schema {
  const next: Schema = { ...ROOT_DEFAULTS, ...(schema ?? {}) }
  next.properties = propsOf(schema)
  const req = requiredOf(schema)
  const idx = req.indexOf(name)
  const out = idx >= 0 ? req.filter((n) => n !== name) : [...req, name]
  if (out.length) next.required = out
  else delete next.required
  return next
}

/** null when valid, otherwise a human-readable reason. */
export function validateFieldName(name: string, taken: string[], originalName?: string): string | null {
  const n = name.trim()
  if (!n) return 'Field name is required'
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(n)) {
    return 'Use letters, numbers, and underscores; must not start with a number'
  }
  if (n !== originalName && taken.includes(n)) return 'A field with that name already exists'
  return null
}

/** Short type badge for the field list ("enum(4)", "string/date"). */
export function describeSpec(spec: Spec): string {
  const t = typeof spec.type === 'string' ? spec.type : 'any'
  if (Array.isArray(spec.enum)) return `enum(${spec.enum.length})`
  if (spec.format) return `${t}/${String(spec.format)}`
  return t
}

function isNum(v: unknown): v is number { return typeof v === 'number' && Number.isFinite(v) }
function num(v: unknown): number | undefined { return isNum(v) ? v : undefined }

/** Fallback label for a field with no `title`: snake/kebab → sentence
 *  case ("amount_usd" → "Amount usd"). Keeps all-caps tokens intact
 *  so "vat_id" reads "Vat id" but "VAT" stays "VAT". */
export function humanizeFieldKey(key: string): string {
  const words = key.replace(/[_-]+/g, ' ').trim().split(/\s+/)
  return words
    .map((w, i) => (w === w.toUpperCase() && w.length > 1 ? w : i === 0 ? w.charAt(0).toUpperCase() + w.slice(1) : w))
    .join(' ')
}

/** The label to show for a schema property. */
export function fieldLabel(key: string, spec: Record<string, unknown> | undefined): string {
  const t = spec?.title
  return typeof t === 'string' && t.trim() ? t.trim() : humanizeFieldKey(key)
}
