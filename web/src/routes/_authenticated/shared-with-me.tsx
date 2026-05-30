import { useMemo } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
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
import type { SharedFolder } from '@/api/workspaces'

// /shared-with-me — every folder in the tenant the caller has been
// granted access to (directly or via a group), grouped by workspace.
// Owner exclusion is server-side (own folders never appear here).
// Click-through deep-links to /workspaces/{wsId}?folder={folderId};
// the workspace route already supports that search param.
function SharedWithMePage() {
  const { t } = useTranslation('folders')
  const navigate = useNavigate()
  const { data, isLoading, isError } = useSharedWithMe()

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
        <EmptyState
          icon={<Inbox className="h-8 w-8" />}
          title={t('shared_with_me.empty_title')}
          description={t('shared_with_me.empty_description')}
        />
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
