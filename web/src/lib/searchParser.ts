// Field-syntax parser for the search page. Tokens in the form
// `field:value` are extracted from the raw query string; the
// remaining free text becomes the query sent to OpenSearch.
//
// Kept in sync with the backend's SearchFilters model in
// services/search/internal/model/model.go.

export interface FieldBag {
  tag?: string[]
  author?: string[]
  classification?: string[]
  region_pin?: string[]
  lifecycle_state?: string[]
  mime_type?: string[]
  workspace_id?: string
}

const FIELD_ALIASES: Record<string, keyof FieldBag> = {
  status: 'lifecycle_state',
  state: 'lifecycle_state',
  lifecycle: 'lifecycle_state',
  tag: 'tag',
  label: 'tag',
  type: 'mime_type',
  mime: 'mime_type',
  author: 'author',
  by: 'author',
  region: 'region_pin',
  class: 'classification',
  workspace: 'workspace_id',
}

// Common shorthand → canonical lifecycle_state values from
// services/document/internal/model/document.go
const STATUS_ALIASES: Record<string, string> = {
  approved: 'active',
  hold: 'legal_hold',
  review: 'in_review',
  reviewing: 'in_review',
}

// Common file-type shorthand → MIME type strings
const MIME_ALIASES: Record<string, string> = {
  pdf: 'application/pdf',
  word: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  excel: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  pptx: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
}

/**
 * Tokenize a raw search string and extract `field:value` pairs.
 *
 * A token is only extracted if it is NOT the last token in the
 * string, OR if the string ends with whitespace (meaning the user
 * intentionally completed the token). This prevents mid-typing
 * extraction of incomplete tokens like `status:in_re`.
 */
export function parseFieldSyntax(raw: string): { free: string; fields: FieldBag } {
  const fields: FieldBag = {}
  const freeParts: string[] = []
  const endsWithSpace = /\s$/.test(raw)
  const tokens = raw.trim().split(/\s+/).filter(Boolean)

  tokens.forEach((token, idx) => {
    const isLast = idx === tokens.length - 1
    const colonIdx = token.indexOf(':')

    if (colonIdx > 0 && (!isLast || endsWithSpace)) {
      const fieldRaw = token.slice(0, colonIdx).toLowerCase()
      const valueRaw = token.slice(colonIdx + 1)
      const fieldKey = FIELD_ALIASES[fieldRaw]

      if (fieldKey && valueRaw) {
        let value = valueRaw.toLowerCase()
        if (fieldKey === 'lifecycle_state') {
          value = STATUS_ALIASES[value] ?? value
        } else if (fieldKey === 'mime_type') {
          value = MIME_ALIASES[value] ?? valueRaw
        }

        if (fieldKey === 'workspace_id') {
          fields.workspace_id = value
        } else if (fieldKey === 'tag') {
          fields.tag = [...(fields.tag ?? []), value]
        } else if (fieldKey === 'author') {
          fields.author = [...(fields.author ?? []), value]
        } else if (fieldKey === 'classification') {
          fields.classification = [...(fields.classification ?? []), value]
        } else if (fieldKey === 'region_pin') {
          fields.region_pin = [...(fields.region_pin ?? []), value]
        } else if (fieldKey === 'lifecycle_state') {
          fields.lifecycle_state = [...(fields.lifecycle_state ?? []), value]
        } else if (fieldKey === 'mime_type') {
          fields.mime_type = [...(fields.mime_type ?? []), value]
        }
        return
      }
    }

    freeParts.push(token)
  })

  return { free: freeParts.join(' '), fields }
}
