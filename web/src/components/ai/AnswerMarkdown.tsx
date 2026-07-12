// Shared renderer for AI-generated answer text (Ask/RAG page, doc Q&A
// tab, chat panel). LLM answers arrive as GitHub-flavored Markdown;
// before this component the app interpolated them as plain strings, so
// users saw raw `**bold**` asterisks and `-` bullets.
//
// Inline citation tokens like `[<doc-uuid>:page_3]` survive as badge
// chips: the text is preprocessed so each known token becomes a
// `citation:` pseudo-link, which the `a` component override swaps for
// whatever `renderCitation` returns. The preprocessing keeps the
// behavior the old plain-text renderer had — adjacent duplicate chips
// collapse, whitespace before trailing punctuation after a chip is
// tightened, and tokens for documents missing from the citation list
// stay as raw text instead of becoming broken links.
import type { ReactNode } from 'react'
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkBreaks from 'remark-breaks'

const CITATION_RE = /\[([0-9a-f-]{8,}(?::page_\d+)?)\]/gi
const CITATION_PROTO = 'citation:'

// Tighten a space the model leaves before trailing punctuation after a
// citation ("[1·p1] ." → "[1·p1].").
const tightenAfterChip = (s: string) => s.replace(/^[ \t]+([.,;:!?)\]…])/, '$1')

export interface AnswerMarkdownProps {
  text: string
  className?: string
  // Returns the chip to render for a known inline citation token
  // (`<doc-uuid>` or `<doc-uuid>:page_N`). Return null/undefined to
  // fall back to the raw `[token]` text.
  renderCitation?: (token: string) => ReactNode
  // Whether a doc id has citation data. Tokens failing this stay raw
  // text and are excluded from adjacent-duplicate collapsing, matching
  // the pre-Markdown renderer. Defaults to "all tokens are known".
  isCitation?: (docId: string) => boolean
}

// Rewrites citation tokens into `[token](citation:token)` Markdown links
// so they survive the Markdown parse as elements we can intercept.
function linkifyCitations(text: string, isCitation: (docId: string) => boolean): string {
  const re = new RegExp(CITATION_RE.source, 'gi')
  let out = ''
  let lastIdx = 0
  let prevChipKey: string | null = null
  let prevWasChip = false
  let m: RegExpExecArray | null
  while ((m = re.exec(text)) !== null) {
    let gap = text.slice(lastIdx, m.index)
    const token = m[1]
    const [docId, pagePart] = token.split(':page_')
    const known = isCitation(docId)
    const chipKey = known ? `${docId}:${pagePart ?? ''}` : null

    // Merge adjacent duplicate chips: same doc+page with only
    // whitespace between them collapses to a single chip.
    if (known && chipKey === prevChipKey && /^\s*$/.test(gap)) {
      lastIdx = m.index + m[0].length
      continue
    }

    if (gap) {
      if (prevWasChip) gap = tightenAfterChip(gap)
      out += gap
    }

    if (known) {
      out += `[${token}](${CITATION_PROTO}${token})`
      prevChipKey = chipKey
      prevWasChip = true
    } else {
      out += m[0]
      prevChipKey = null
      prevWasChip = false
    }
    lastIdx = m.index + m[0].length
  }
  let tail = text.slice(lastIdx)
  if (tail && prevWasChip) tail = tightenAfterChip(tail)
  return out + tail
}

export function AnswerMarkdown({ text, className, renderCitation, isCitation }: AnswerMarkdownProps) {
  const source = renderCitation ? linkifyCitations(text, isCitation ?? (() => true)) : text
  return (
    <div className={className}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkBreaks]}
        // The default transform strips unknown protocols; let our
        // citation pseudo-links through, sanitize everything else.
        urlTransform={(url) => (url.startsWith(CITATION_PROTO) ? url : defaultUrlTransform(url))}
        components={{
          a: ({ href, children }) => {
            if (href?.startsWith(CITATION_PROTO) && renderCitation) {
              const chip = renderCitation(href.slice(CITATION_PROTO.length))
              if (chip != null) return <>{chip}</>
              return <>[{children}]</>
            }
            return (
              <a href={href} target="_blank" rel="noopener noreferrer" className="underline">
                {children}
              </a>
            )
          },
        }}
      >
        {source}
      </ReactMarkdown>
    </div>
  )
}
