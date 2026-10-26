import { Link, useRouterState } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Home } from 'lucide-react'
import { Fragment, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import type { Workspace } from '@/types/api'
import { humanizeSegment } from '@/lib/segmentLabels'
import { cn } from '@/lib/cn'

// UUID_RE matches v4/v7-style UUIDs as they appear in route params.
const UUID_RE = /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i

// Segments that exist only as URL structure — there is no route at that
// prefix, so linking them ejects the user onto a shell-less 404.
const STRUCTURAL_SEGMENTS = new Set(['documents', 'instances'])

// findIdAfter — return segments[i+1] when segments[i] === marker AND
// that following segment is a UUID. Lets us subscribe to a specific
// entity's React Query cache key for the visible URL.
function findIdAfter(segments: string[], marker: string): string | undefined {
  const i = segments.indexOf(marker)
  if (i < 0) return undefined
  const next = segments[i + 1]
  if (!next || !UUID_RE.test(next)) return undefined
  return next
}

export function Breadcrumbs() {
  // Subscribes this component to 'languageChanged'. humanizeSegment is a
  // plain function reading the i18next singleton, so without a subscriber
  // the crumbs keep whatever label they were memoised with — which meant
  // the first page after login rendered English crumbs under an Arabic UI.
  const { t, i18n } = useTranslation('common')
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const segments = useMemo(() => pathname.split('/').filter(Boolean), [pathname])

  // Extract the IDs the URL surfaces. Subscribing via useQuery with
  // `enabled: false` gives us a reactive read of whatever the
  // detail-page consumers / sidebar list have already populated;
  // we never fire a fetch from the crumb itself. When the URL has
  // no id of a given kind, we still call useQuery (hook-order rule)
  // with a sentinel key so the hook doesn't read garbage.
  const workspaceId = findIdAfter(segments, 'workspaces')
  const documentId = findIdAfter(segments, 'documents')
  const workflowId = findIdAfter(segments, 'instances')

  // Subscribe-only: `enabled: false` keeps the breadcrumb from
  // firing its own fetch — it reads whatever the page-level
  // consumers have already cached. v5 still requires `queryFn` to
  // be declared even on disabled queries, hence the stub.
  const noopQuery = () => Promise.resolve(undefined as never)
  const workspace = useQuery<{ name?: string }>({
    queryKey: ['workspace', workspaceId ?? '__none__'],
    queryFn: noopQuery,
    enabled: false,
  })
  const workspacesList = useQuery<Workspace[]>({
    queryKey: ['workspaces'],
    queryFn: noopQuery,
    enabled: false,
  })
  const documentQ = useQuery<{ title?: string }>({
    queryKey: ['document', documentId ?? '__none__'],
    queryFn: noopQuery,
    enabled: false,
  })
  const workflow = useQuery<{ definition_name?: string }>({
    queryKey: ['workflow-instance', workflowId ?? '__none__'],
    queryFn: noopQuery,
    enabled: false,
  })

  // Workspace name lookup: prefer the detail-page cache, then fall
  // back to scanning the sidebar's ['workspaces'] list. The list is
  // hydrated on every authenticated page via WorkspaceSelector +
  // the dashboard KPI row, so it's nearly always available even
  // when the workspace detail page was never visited.
  const workspaceName =
    workspace.data?.name ??
    workspacesList.data?.find((w) => w.id === workspaceId)?.name

  const crumbs = useMemo(() => {
    let href = ''
    return segments.map((segment) => {
      href += '/' + segment
      let label: string | undefined
      if (segment === workspaceId) label = workspaceName
      else if (segment === documentId) label = documentQ.data?.title
      else if (segment === workflowId) label = workflow.data?.definition_name
      return { segment, href, label: label ?? humanizeSegment(segment) }
    })
  }, [
    // i18n.language is a real dependency: the labels come from the
    // catalogue, so they must be recomputed when the locale changes.
    i18n.language,
    segments,
    workspaceId,
    workspaceName,
    documentId,
    documentQ.data?.title,
    workflowId,
    workflow.data?.definition_name,
  ])

  return (
    <nav aria-label={t('a11y.breadcrumb')} className="flex min-w-0 items-center text-sm">
      {/* On the dashboard there is no deeper crumb, so Home is itself the
          current page: foreground colour + aria-current, as the last crumb
          below gets. */}
      <Link
        to="/"
        aria-current={crumbs.length === 0 ? 'page' : undefined}
        className={cn(
          'flex shrink-0 items-center gap-1.5 transition-colors hover:text-foreground',
          crumbs.length === 0 ? 'font-semibold text-foreground' : 'text-muted-foreground',
        )}
      >
        <Home className="h-4 w-4" aria-hidden />
        <span>{t('header.home')}</span>
      </Link>
      {crumbs.map((crumb, idx) => {
        const isLast = idx === crumbs.length - 1
        return (
          <Fragment key={crumb.href}>
            <DirectionalIcon name="ChevronRight" className="mx-1 h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
            {isLast ? (
              <span
                className="block max-w-[32ch] truncate font-semibold text-foreground sm:max-w-[56ch]"
                title={crumb.label}
              >
                {crumb.label}
              </span>
            ) : STRUCTURAL_SEGMENTS.has(crumb.segment) ? (
              // No route lives at this prefix — render as plain text so
              // the crumb can't eject the user onto a shell-less 404.
              <span
                className="block max-w-[28ch] truncate text-muted-foreground sm:max-w-[40ch]"
                title={crumb.label}
              >
                {crumb.label}
              </span>
            ) : (
              <Link
                to={crumb.href}
                className="block max-w-[28ch] truncate text-muted-foreground transition-colors hover:text-foreground sm:max-w-[40ch]"
                title={crumb.label}
              >
                {crumb.label}
              </Link>
            )}
          </Fragment>
        )
      })}
    </nav>
  )
}
