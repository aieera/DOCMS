// Per-route <title> resolution.
//
// Every route used to render as the bare app name, so browser tabs,
// history entries and screen-reader page announcements were all
// indistinguishable.
//
// Resolution order for the page name, deepest match first:
//   1. A route's own `head()` meta title — the TanStack Router
//      mechanism. Nothing declares one today, but it is the supported
//      extension point for a route that needs a *dynamic* title (e.g.
//      the document page naming the open document):
//
//        export const Route = createFileRoute('/…')({
//          head: () => ({ meta: [{ title: 'My page' }] }),
//        })
//
//   2. ROUTE_TITLES below, keyed by route id — corrections for routes
//      whose URL cannot produce a sensible name (dynamic `$param`
//      segments, sign-in flows).
//   3. The last non-opaque URL segment, run through the same label
//      table the breadcrumb uses. This is what covers the long tail of
//      admin pages without a 100-entry registry to keep in sync.
//
// Why this is applied imperatively (see DocumentTitle in routes/__root)
// rather than by rendering TanStack's <HeadContent />: React 18 does not
// hoist a <title> element out of the component tree into <head> — that
// landed in React 19. Until the app is on 19, assigning document.title
// is the only reliable option.
import { humanizeSegment, looksLikeId } from './segmentLabels'

export const APP_NAME = 'SeDoc'

const ROUTE_TITLES: Record<string, string> = {
  '/_authenticated/': 'Home',
  '/_authenticated/workspaces/$workspaceId/': 'Workspace',
  '/_authenticated/workspaces/$workspaceId/settings': 'Workspace settings',
  '/_authenticated/workflows/$templateId/edit': 'Edit workflow',
  '/_authenticated/workflows/instances/$instanceId': 'Workflow run',
  '/_authenticated/sign/$requestId/$signerId': 'Sign document',
  '/_authenticated/sign/in-person/$requestId': 'Sign document',
  '/_authenticated/sign/done': 'Signing complete',
  '/_authenticated/signatures/send/$documentId': 'Send for signature',
  '/_authenticated/sandbox/crextio': 'Component sandbox',
  '/protected/$containerId': 'Protected document',
  '/shared/$token': 'Shared document',
  '/zt/$token': 'Secure document view',
  '/login': 'Sign in',
  '/register': 'Create account',
  '/forgot-password': 'Reset password',
  '/accept-invite': 'Accept invitation',
}

/** Minimal shape of a router match — kept structural so the resolver is
 *  unit-testable without constructing a real router. */
export interface TitleMatch {
  routeId: string
  meta?: ReadonlyArray<{ title?: string } | undefined | null>
}

export function formatDocumentTitle(page?: string | null): string {
  const trimmed = page?.trim()
  return trimmed && trimmed !== APP_NAME ? `${trimmed} · ${APP_NAME}` : APP_NAME
}

export function pageNameFor(
  matches: ReadonlyArray<TitleMatch>,
  pathname: string,
): string | undefined {
  for (let i = matches.length - 1; i >= 0; i--) {
    const match = matches[i]
    const metaTitle = match.meta?.find((m) => m?.title)?.title
    if (metaTitle) return metaTitle
    const mapped = ROUTE_TITLES[match.routeId]
    if (mapped) return mapped
  }
  const segments = pathname.split('/').filter(Boolean)
  if (segments.length === 0) return 'Home'
  for (let i = segments.length - 1; i >= 0; i--) {
    if (!looksLikeId(segments[i])) return humanizeSegment(segments[i])
  }
  return undefined
}

export function documentTitleFor(
  matches: ReadonlyArray<TitleMatch>,
  pathname: string,
): string {
  return formatDocumentTitle(pageNameFor(matches, pathname))
}
