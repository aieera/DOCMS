import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { GitBranch, AlertTriangle, Plus } from 'lucide-react'

import { getWorkflowDefinitions, type WorkflowDefinition } from '@/api/workflows'
import { PageHeader } from '@/components/shared/PageHeader'
import { WorkflowGraph } from '@/components/shared/WorkflowGraph'
import { Skeleton } from '@/components/ui/Skeleton'
import { Button } from '@/components/ui/shadcn/button'

function WorkflowsPage() {
  const { data, isLoading, isError, error, refetch, isFetching } = useQuery({
    queryKey: ['workflow-definitions'],
    queryFn: getWorkflowDefinitions,
    retry: 1,
  })
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const selected: WorkflowDefinition | undefined =
    data?.find((d) => d.id === selectedId) ?? data?.[0]

  if (isLoading) {
    return (
      <div>
        <PageHeader title="Workflows" description="Define and manage approval workflows" />
        <div className="grid grid-cols-[260px_1fr] gap-4">
          <ul className="space-y-2">
            {Array.from({ length: 4 }).map((_, i) => (
              <li key={i} className="rounded-md border border-border p-3">
                <Skeleton className="h-4 w-3/4" />
                <Skeleton className="mt-2 h-3 w-1/3" />
              </li>
            ))}
          </ul>
          <div className="rounded-md border border-border p-4">
            <Skeleton className="h-5 w-1/3" />
            <Skeleton className="mt-3 h-48 w-full" />
          </div>
        </div>
      </div>
    )
  }

  if (isError) {
    return (
      <div>
        <PageHeader title="Workflows" description="Define and manage approval workflows" />
        <div
          className="flex flex-col items-center justify-center rounded-lg border border-destructive/40 bg-destructive/5 p-12 text-center"
          data-testid="workflows-error"
        >
          <AlertTriangle className="h-10 w-10 text-destructive" />
          <h3 className="mt-4 text-lg font-medium">Could not load workflows</h3>
          <p className="mt-1 max-w-md text-sm text-muted-foreground">
            {error instanceof Error ? error.message : 'Server error — please retry.'}
          </p>
          <Button className="mt-4" onClick={() => refetch()} loading={isFetching} data-testid="workflows-retry">
            Retry
          </Button>
        </div>
      </div>
    )
  }

  if (!data || data.length === 0) {
    return (
      <div>
        <PageHeader title="Workflows" description="Define and manage approval workflows" />
        <div className="flex flex-col items-center justify-center rounded-xl border border-border bg-card p-12 text-center shadow-sm">
          <span className="flex h-14 w-14 items-center justify-center rounded-full bg-primary/10 text-primary">
            <GitBranch className="h-7 w-7" />
          </span>
          <h3 className="mt-4 text-lg font-medium">No workflows defined</h3>
          <p className="mt-1 max-w-md text-sm text-muted-foreground">
            Build approval, parallel, and signature workflows in the visual designer.
          </p>
          <Button
            asChild
            className="mt-5 gap-2 shadow-sm transition-all hover:shadow-md hover:ring-2 hover:ring-primary/20 hover:ring-offset-2 hover:ring-offset-background"
          >
            <Link to="/workflows/designer" data-testid="open-designer">
              <Plus className="h-4 w-4" /> Open designer
            </Link>
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div>
      <PageHeader title="Workflows" description="Define and manage approval workflows" />
      <div className="grid grid-cols-[260px_1fr] gap-4">
        <ul className="space-y-1">
          {data.map((def) => {
            const active = (selected?.id ?? data[0].id) === def.id
            return (
              <li key={def.id}>
                <button
                  onClick={() => setSelectedId(def.id)}
                  className={`w-full rounded-md px-3 py-2 text-start text-sm ${
                    active
                      ? 'bg-[var(--color-accent)] text-[var(--color-primary)]'
                      : 'hover:bg-[var(--color-bg-secondary)]'
                  }`}
                >
                  <div className="font-medium">{def.name}</div>
                  <div className="text-xs text-[var(--color-text-secondary)]">
                    {def.steps.length} step{def.steps.length === 1 ? '' : 's'}
                  </div>
                </button>
              </li>
            )
          })}
        </ul>
        <div>
          {selected && (
            <>
              <div className="mb-2">
                <h2 className="text-lg font-semibold">{selected.name}</h2>
                {selected.description && (
                  <p className="text-sm text-[var(--color-text-secondary)]">{selected.description}</p>
                )}
              </div>
              <WorkflowGraph steps={selected.steps} />
            </>
          )}
        </div>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/workflows')({ component: WorkflowsPage })
