import { useMemo } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  Folder as FolderIcon,
  Inbox,
  Users as GroupIcon,
  User as UserIcon,
} from 'lucide-react'

import { useSharedWithMe } from '@/hooks/useFolders'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/Skeleton'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { FileIcon } from '@/components/ui/FileIcon'
import { getMyGrants } from '@/api/permissions'
import { getDocument } from '@/api/documents'
import { runBatched } from '@/lib/runBatched'
import type { SharedFolder } from '@/api/workspaces'
import type { Document } from '@/types/api'

// Documents shared directly with the caller: the policy service's
// self-scoped grants (`/permissions/mine`) name the document ids; the
// document service supplies title/mime/workspace for each. Grants whose
// document has since been deleted (or is otherwise unreadable) are
// dropped silently — a stale grant row isn't the reader's problem.
function useSharedDocuments() {
  return useQuery({
    queryKey: ['shared-with-me', 'documents'],
    queryFn: async () => {
      const grants = await getMyGrants()
      const docGrants = new Map<string, { capability: string }>()
      for (const g of grants) {
        if (g.resource_type !== 'document') continue
        if (!docGrants.has(g.resource_id)) docGrants.set(g.resource_id, { capability: g.capability })
      }
      const ids = [...docGrants.keys()].slice(0, 50)
      const docs: Array<{ doc: Document; capability: string }> = []
      await runBatched(ids, async (id: string) => {
        const doc = await getDocument(id)
        docs.push({ doc, capability: docGrants.get(id)!.capability })
      }, { concurrency: 5 })
      return docs
    },
    staleTime: 30_000,
  })
}

// /shared-with-me — every folder in the tenant the caller has been
// granted access to (directly or via a group), grouped by workspace.
// Owner exclusion is server-side (own folders never appear here).
// Click-through deep-links to /workspaces/{wsId}?folder={folderId};
// the workspace route already supports that search param.
function SharedWithMePage() {
  const { t } = useTranslation('folders')
  const navigate = useNavigate()
  const { data, isLoading, isError } = useSharedWithMe()
  const sharedDocs = useSharedDocuments()

  const grouped = useMemo(() => {
    const map = new Map<string, { workspaceName: string; items: SharedFolder[] }>()
    for (const item of data ?? []) {
      const key = item.workspace.id
      const entry = map.get(key)
      if (entry) {
        entry.items.push(item)
      } else {
        map.set(key, { workspaceName: item.workspace.name, items: [item] })
      }
    }
    // Stable order: workspace name asc.
    return Array.from(map.entries())
      .map(([wsId, v]) => ({ wsId, ...v }))
      .sort((a, b) => a.workspaceName.localeCompare(b.workspaceName))
  }, [data])

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('shared_with_me.title')}
        description={t('shared_with_me.description')}
      />

      {/* ---- documents shared directly with me ---- */}
      {(sharedDocs.data?.length ?? 0) > 0 && (
        <section aria-labelledby="shared-docs-heading">
          <h2
            id="shared-docs-heading"
            className="mb-3 text-xs font-medium uppercase tracking-wide text-muted-foreground"
          >
            Documents
          </h2>
          <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3" data-testid="shared-docs-list">
            {sharedDocs.data!.map(({ doc, capability }) => (
              <li key={doc.id}>
                <div
                  role="button"
                  tabIndex={0}
                  onClick={() =>
                    navigate({
                      to: '/workspaces/$workspaceId/documents/$documentId',
                      params: { workspaceId: doc.workspace_id, documentId: doc.id },
                    })
                  }
                  onKeyDown={(e) => {
                    if (e.target !== e.currentTarget) return // portal-bubbled (dialog/menu) keys are not activation
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      navigate({
                        to: '/workspaces/$workspaceId/documents/$documentId',
                        params: { workspaceId: doc.workspace_id, documentId: doc.id },
                      })
                    }
                  }}
                  className="group flex cursor-pointer items-start gap-3 rounded-xl border border-border bg-card p-3 transition-all hover:border-primary/40 hover:bg-muted/40"
                  data-testid={`shared-doc-${doc.id}`}
                  aria-label={`Open ${doc.title}`}
                >
                  <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10">
                    <FileIcon mime={doc.mime_type} className="h-5 w-5" />
                  </span>
                  <div className="min-w-0 flex-1">
                    <h3 className="truncate text-sm font-semibold" title={doc.title}>{doc.title}</h3>
                    <div className="mt-1.5">
                      <Badge variant="outline" className="text-[10px] capitalize">{capability}</Badge>
                    </div>
                  </div>
                </div>
              </li>
            ))}
          </ul>
        </section>
      )}

      {isLoading ? (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-20 rounded-xl" />
          ))}
        </div>
      ) : isError ? (
        <Card className="p-6 text-sm text-muted-foreground">
          {t('shared_with_me.error')}
        </Card>
      ) : grouped.length === 0 ? (
        (sharedDocs.data?.length ?? 0) === 0 && !sharedDocs.isLoading ? (
          <EmptyState
            icon={<Inbox className="h-8 w-8" />}
            title={t('shared_with_me.empty_title')}
            description={t('shared_with_me.empty_description')}
          />
        ) : null
      ) : (
        <div className="space-y-8" data-testid="shared-with-me-list">
          {grouped.map((group) => (
            <section key={group.wsId} aria-labelledby={`ws-${group.wsId}`}>
              <h2
                id={`ws-${group.wsId}`}
                className="mb-3 text-xs font-medium uppercase tracking-wide text-muted-foreground"
              >
                {group.workspaceName}
              </h2>
              <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {group.items.map((item) => (
                  <li key={item.folder.id}>
                    <SharedFolderCard
                      item={item}
                      onOpen={() =>
                        navigate({
                          to: '/workspaces/$workspaceId',
                          params: { workspaceId: item.workspace.id },
                          search: { folder: item.folder.id },
                        })
                      }
                    />
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      )}
    </div>
  )
}

interface CardProps {
  item: SharedFolder
  onOpen: () => void
}

function SharedFolderCard({ item, onOpen }: CardProps) {
  const { t } = useTranslation('folders')
  const viaGroup = item.granted_via === 'group'
  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return // portal-bubbled (dialog/menu) keys are not activation
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onOpen()
        }
      }}
      className="group flex cursor-pointer items-start gap-3 rounded-xl border border-border bg-card p-3 transition-all hover:border-primary/40 hover:bg-muted/40"
      data-testid={`shared-folder-${item.folder.id}`}
      aria-label={t('shared_with_me.open_aria', { name: item.folder.name })}
    >
      <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <FolderIcon className="h-5 w-5" />
      </span>
      <div className="min-w-0 flex-1">
        <h3 className="truncate text-sm font-semibold" title={item.folder.name}>
          {item.folder.name}
        </h3>
        <p className="mt-0.5 truncate text-xs text-muted-foreground">
          {item.workspace.name}
        </p>
        <div className="mt-1.5 flex items-center gap-1.5 text-[10px]">
          {viaGroup ? (
            <span
              className="inline-flex items-center gap-0.5 rounded-full bg-muted px-1.5 py-0 text-muted-foreground"
              title={t('shared_with_me.via_group_tooltip')}
            >
              <GroupIcon className="h-3 w-3" />
              {t('shared_with_me.via_group')}
            </span>
          ) : (
            <span className="inline-flex items-center gap-0.5 rounded-full bg-muted px-1.5 py-0 text-muted-foreground">
              <UserIcon className="h-3 w-3" />
              {t('shared_with_me.via_user')}
            </span>
          )}
        </div>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/shared-with-me')({
  component: SharedWithMePage,
})
