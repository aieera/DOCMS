import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { GitBranch } from 'lucide-react'

import { getWorkflowDefinitions, type WorkflowDefinition } from '@/api/workflows'
import { PageHeader } from '@/components/shared/PageHeader'
import { WorkflowGraph } from '@/components/shared/WorkflowGraph'
import { Skeleton } from '@/components/ui/Skeleton'

function WorkflowsPage() {
  const { data, isLoading } = useQuery({
    queryKey: ['workflow-definitions'],
    queryFn: getWorkflowDefinitions,
  })
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const selected: WorkflowDefinition | undefined =
    data?.find((d) => d.id === selectedId) ?? data?.[0]

  if (isLoading) {
    return (
      <div>
        <PageHeader title="Workflows" description="Define and manage approval workflows" />
        <Skeleton className="h-64" />
      </div>
    )
  }

  if (!data || data.length === 0) {
    return (
      <div>
        <PageHeader title="Workflows" description="Define and manage approval workflows" />
        <div className="flex flex-col items-center justify-center rounded-lg border border-dashed p-12 text-center">
          <GitBranch className="h-10 w-10 text-[var(--color-text-secondary)]" />
          <h3 className="mt-4 text-lg font-medium">No workflows defined</h3>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
            Create workflows via API to automate document approval processes.
            Drag-to-create UI is on the post-G3 roadmap.
          </p>
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
                  className={`w-full rounded-md px-3 py-2 text-left text-sm ${
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
