// Differential RTL layout detector, shared by the RTL geometry gate.
//
// ADR 0107 moved the whole app onto logical Tailwind utilities, and
// `npm run lint:rtl` stops physical ones coming back. Neither checks
// what the browser actually lays out: a flex child with no `min-w-0`
// refuses to shrink below its content in either direction, and the
// leftover space lands on whichever side `dir` puts it. That is how the
// topbar's ⌘K chip ended up sitting on the notification badges in
// Arabic while looking perfect in English.
//
// The method is differential on purpose. Measuring RTL alone reports
// every quirk the layout already has in English; measuring both and
// subtracting leaves only what flipping direction broke.
//
// Two things make the subtraction sound:
//   * elements are identified by their nth-child chain, never by text —
//     text is what changes between languages, so a text key makes every
//     element look new in the other direction;
//   * rects are intersected with every clipping ancestor before they are
//     compared, so content that overflow:hidden has already cut off is
//     not reported as an overlap. (A `truncate` cell legitimately lets
//     its inline children run past the box; nobody sees them.)

export interface RtlFindings {
  overflow: string[]
  overlap: string[]
}

// Runs inside the page. Must stay self-contained — Playwright serialises
// it, so it cannot close over anything in this module.
export const detectLayout = (): RtlFindings => {
  const vw = document.documentElement.clientWidth
  const vh = document.documentElement.clientHeight

  const path = (e: Element): string => {
    const p: string[] = []
    for (let n: Element | null = e; n && n !== document.body; n = n.parentElement) {
      const i = n.parentElement ? Array.from(n.parentElement.children).indexOf(n) : 0
      p.unshift(`${n.tagName.toLowerCase()}:${i}`)
    }
    return p.join('>')
  }

  const clips = (s: CSSStyleDeclaration) =>
    s.overflow !== 'visible' || s.overflowX !== 'visible' || s.overflowY !== 'visible'

  // The box a human actually sees: the element's own rect intersected
  // with every clipping ancestor.
  const seen = (e: Element) => {
    const r = e.getBoundingClientRect()
    let left = r.left, right = r.right, top = r.top, bottom = r.bottom
    for (let n = e.parentElement; n && n !== document.documentElement; n = n.parentElement) {
      if (!clips(getComputedStyle(n))) continue
      const p = n.getBoundingClientRect()
      left = Math.max(left, p.left); right = Math.min(right, p.right)
      top = Math.max(top, p.top); bottom = Math.min(bottom, p.bottom)
    }
    return { left, right, top, bottom, w: right - left, h: bottom - top }
  }

  const visible = (e: Element) => {
    const s = getComputedStyle(e)
    if (s.display === 'none' || s.visibility === 'hidden' || s.opacity === '0') return false
    const c = seen(e)
    return c.w > 1 && c.h > 1 && c.top < vh && c.bottom > 0 && c.left < vw && c.right > 0
  }

  // Icons draw outside their own box by design; the <svg> box is what
  // participates in layout, so measure that and skip its internals.
  const els = Array.from(document.querySelectorAll('body *')).filter(
    (e) => !e.closest('svg') && visible(e),
  )

  // Leaves only. Nested containers overlap their ancestors as a matter
  // of course; two leaves sharing pixels is text sitting on text.
  const leaves = els.filter(
    (e) => !Array.from(e.children).some((c) => c.nodeType === 1 && visible(c)),
  )

  // Measure every leaf ONCE. Calling seen() inside the pair loop costs a
  // forced layout per comparison — O(n^2) reflows turn a dense table
  // into a multi-second scan.
  //
  // Inline elements are measured per LINE FRAGMENT, not by their union
  // box. An inline that wraps over two lines has a bounding rect
  // covering both, which then overlaps whatever sits beside it on the
  // first line — every wrapped label would read as a collision. Arabic
  // wraps in different places from English, so the union box made
  // ordinary rewrapping look like breakage.
  const box = new Map<Element, ReturnType<typeof seen>[]>()
  for (const e of leaves) {
    const own = seen(e)
    const rects = Array.from(e.getClientRects())
    if (rects.length < 2) { box.set(e, [own]); continue }
    // Clip each fragment to the same window seen() computed, so the
    // clipping-ancestor logic still applies.
    box.set(e, rects.map((r) => ({
      left: Math.max(r.left, own.left), right: Math.min(r.right, own.right),
      top: Math.max(r.top, own.top), bottom: Math.min(r.bottom, own.bottom),
      w: 0, h: 0,
    })).filter((r) => r.right - r.left > 1 && r.bottom - r.top > 1))
  }
  const hit = (a: ReturnType<typeof seen>[], b: ReturnType<typeof seen>[]) =>
    a.some((x) => b.some((y) =>
      Math.min(x.right, y.right) - Math.max(x.left, y.left) > 1 &&
      Math.min(x.bottom, y.bottom) - Math.max(x.top, y.top) > 1))

  // Overlay layers float over the page on purpose, so a toast sitting on
  // a form field is not a finding — and toasts fire on their own
  // schedule, so comparing them against page content makes the whole
  // check nondeterministic. Two leaves are only compared when they
  // belong to the SAME layer; text colliding inside one dialog is still
  // a bug worth reporting.
  const OVERLAY = [
    '[data-sonner-toaster]', '[aria-live]', '[role="alert"]', '[role="status"]',
    '[role="dialog"]', '[role="alertdialog"]', '[role="tooltip"]', '[role="menu"]',
    '[role="listbox"]', '[data-radix-popper-content-wrapper]',
  ].join(',')
  const layer = (e: Element): Element | null => e.closest(OVERLAY)

  const overflow: string[] = []
  const overlap: string[] = []

  for (const e of leaves) {
    if (getComputedStyle(e).position === 'fixed') continue
    const c = seen(e)
    if (c.left < -2 || c.right > vw + 2) overflow.push(`VIEWPORT ${path(e)}`)
    // Escaping a parent that does NOT clip is the topbar bug class:
    // visible content resting outside the container that owns it.
    const par = e.parentElement
    if (par && !clips(getComputedStyle(par))) {
      const p = seen(par)
      if (p.w > 2 && (c.left < p.left - 2 || c.right > p.right + 2)) {
        overflow.push(`PARENT ${path(e)}`)
      }
    }
  }

  for (let i = 0; i < leaves.length; i++) {
    const a = leaves[i]
    const ra = box.get(a)!
    for (let j = i + 1; j < leaves.length; j++) {
      const b = leaves[j]
      // Cheap rectangle reject before the DOM walks, which are the
      // expensive half of this loop.
      if (!hit(ra, box.get(b)!)) continue
      if (a.contains(b) || b.contains(a)) continue
      if (layer(a) !== layer(b)) continue
      overlap.push(`${path(a)} X ${path(b)}`)
    }
  }

  return { overflow: Array.from(new Set(overflow)), overlap: Array.from(new Set(overlap)) }
}

// What RTL broke that LTR did not. Both sides must be measured with the
// same mock data, or the DOM paths do not line up and everything reads
// as new.
export function rtlOnly(ltr: RtlFindings, rtl: RtlFindings): string[] {
  return [
    ...rtl.overlap.filter((x) => !ltr.overlap.includes(x)).map((x) => `overlap: ${x}`),
    ...rtl.overflow.filter((x) => !ltr.overflow.includes(x)).map((x) => `overflow: ${x}`),
  ]
}
