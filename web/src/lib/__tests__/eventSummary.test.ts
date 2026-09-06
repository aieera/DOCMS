// QA SD-11: the document Details panel listed raw event-bus topics
// ("dms.notify.document.uploaded.v1 — 2 minutes ago"). Topic names and
// schema versions are implementation detail; anything topic-shaped gets
// a human label, and real sentences pass through untouched.
import { describe, expect, it } from 'vitest'
import { humanizeEventSummary } from '@/lib/eventSummary'

describe('humanizeEventSummary (SD-11)', () => {
  it('maps known topics to product vocabulary', () => {
    expect(humanizeEventSummary('dms.document.created.v1')).toBe('Document created')
    expect(humanizeEventSummary('dms.notify.document.uploaded.v1')).toBe('Content uploaded')
    expect(humanizeEventSummary('dms.version.uploaded.v1')).toBe('New version uploaded')
    expect(humanizeEventSummary('dms.document.deleted.v1')).toBe('Moved to Trash')
    expect(humanizeEventSummary('dms.document.restored.v1')).toBe('Restored from Trash')
  })

  it('prettifies unknown topic-shaped strings instead of leaking them', () => {
    expect(humanizeEventSummary('dms.workflow.step_completed.v2')).toBe('Workflow step completed')
  })

  it('uppercases acronyms when de-slugging (SD-11 retest)', () => {
    // "Ocr quality completed" reads as a typo; the acronym table fixes it.
    expect(humanizeEventSummary('dms.ocr.quality.completed.v1')).toBe('OCR quality completed')
    expect(humanizeEventSummary('dms.pii.finding_raised.v1')).toBe('PII finding raised')
  })

  it('leaves human sentences alone', () => {
    expect(humanizeEventSummary('Signature requested from Manu')).toBe('Signature requested from Manu')
  })
})
