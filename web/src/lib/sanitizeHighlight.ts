// Whitelist filter for search-highlight fragments before they're
// injected as HTML. The search service emits plain text wrapped in
// bare <mark>…</mark> tags and (since the encoder=html fix) escapes
// the document's own text server-side — this is the defense-in-depth
// layer in case a fragment ever arrives from an older index/service:
// every tag that isn't exactly <mark> or </mark> is stripped,
// including <mark> with smuggled attributes.
export function sanitizeHighlight(html: string): string {
  return html.replace(/<(?!\/?mark>)[^>]*>?/gi, '')
}
