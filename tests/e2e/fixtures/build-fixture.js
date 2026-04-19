#!/usr/bin/env node
// Generates the 3-page `sample-contract.pdf` fixture on demand so we
// don't commit binary files. The PDF contains the word CONFIDENTIAL
// which the smoke test searches for after OCR. Produced by hand with
// minimal PDF syntax so this script has zero npm dependencies — it
// runs on any Node 18+ install, no `npm install` needed.
//
// Usage: node build-fixture.js
// Output: sample-contract.pdf (same directory)

import { writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))

// Minimal PDF document, 3 pages, one line of text per page.
// Structure: header, 1=Catalog, 2=Pages, 3-5=Page, 6-8=Content, 9=Font, xref, trailer.

function makeContentStream(text) {
  const body = `BT /F1 18 Tf 72 720 Td (${text}) Tj ET`
  return `<< /Length ${body.length} >>\nstream\n${body}\nendstream`
}

const pageTexts = [
  'CONFIDENTIAL - Sample Contract - Page 1',
  'Section 1: This document is CONFIDENTIAL and subject to NDA.',
  'End of sample. CONFIDENTIAL. Reference ACME-2026-001.',
]

// Build the indirect objects.
const objects = []
objects[0] = null // 1-indexed
objects[1] = `<< /Type /Catalog /Pages 2 0 R >>`
objects[2] = `<< /Type /Pages /Count 3 /Kids [3 0 R 4 0 R 5 0 R] >>`
// Pages 3-5 reference content 6-8 and share font 9.
for (let i = 0; i < 3; i++) {
  const pageObj = 3 + i
  const contentObj = 6 + i
  objects[pageObj] =
    `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] ` +
    `/Resources << /Font << /F1 9 0 R >> >> ` +
    `/Contents ${contentObj} 0 R >>`
  objects[contentObj] = makeContentStream(pageTexts[i])
}
objects[9] = `<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`

// Serialize to bytes, tracking byte offsets for xref.
let pdf = '%PDF-1.4\n%\u00E2\u00E3\u00CF\u00D3\n'
const offsets = [0]
for (let i = 1; i < objects.length; i++) {
  if (!objects[i]) continue
  offsets[i] = Buffer.byteLength(pdf, 'binary')
  pdf += `${i} 0 obj\n${objects[i]}\nendobj\n`
}
const xrefOffset = Buffer.byteLength(pdf, 'binary')
pdf += `xref\n0 ${objects.length}\n`
pdf += '0000000000 65535 f \n'
for (let i = 1; i < objects.length; i++) {
  const off = (offsets[i] ?? 0).toString().padStart(10, '0')
  pdf += `${off} 00000 n \n`
}
pdf += `trailer\n<< /Size ${objects.length} /Root 1 0 R >>\nstartxref\n${xrefOffset}\n%%EOF\n`

const outPath = join(here, 'sample-contract.pdf')
writeFileSync(outPath, pdf, 'binary')
console.log(`wrote ${outPath} (${Buffer.byteLength(pdf, 'binary')} bytes)`)
