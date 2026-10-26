import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { getWorkspaces } from '@/api/workspaces'
import { DashboardUploadDialog } from '@/components/documents/DashboardUploadDialog'
import { useAuthStore } from '@/store/authStore'
import { AskCard } from '@/components/dashboard/AskCard'
import { AttentionList } from '@/components/dashboard/AttentionList'
import { DashboardHead } from '@/components/dashboard/DashboardHead'
import { DocumentsAddedChart } from '@/components/dashboard/DocumentsAddedChart'
import { KpiStrip } from '@/components/dashboard/KpiStrip'
import { LibraryBreakdown } from '@/components/dashboard/LibraryBreakdown'
import { greet } from '@/components/dashboard/greet'

function DashboardPage() {
  const { t } = useTranslation('common')
  const user = useAuthStore((s) => s.user)
  const greeting = greet(t, user?.display_name?.trim().split(/\s+/)[0])
  // Same query (and key) as KpiStrip's, so it is fetched once.
  const workspaces = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces, staleTime: 60_000 })
  // Upload dialog state lives here; the head's Upload button opens it.
  const [uploadOpen, setUploadOpen] = useState(false)
  const [droppedFiles, setDroppedFiles] = useState<File[]>([])

  return (
    // 12 columns, 16px gap. `shell` / `wide` are the 801px / 1181px screens:
    // at or below 800px every cell is full width; from 801px attention and
    // ask take half a row each; from 1181px the chart and the library take
    // 8 columns and attention and ask the other 4.
    // `grid-flow-dense`: between those two screens attention and ask are 6
    // wide but the full-width library card sits between them in the DOM, so
    // without it each would leave half its row empty. Dense packing puts them
    // side by side. At every other width it changes nothing.
    <div className="grid grid-cols-12 gap-4 grid-flow-dense">
      <div className="col-span-12">
        <DashboardHead
          greeting={greeting}
          workspaceCount={workspaces.isSuccess ? workspaces.data.length : undefined}
          onUpload={() => setUploadOpen(true)}
        />
      </div>

      {/* KpiStrip lays out its own cards: 1 / 2 / 4 columns. */}
      <div className="col-span-12">
        <KpiStrip />
      </div>

      {/* Cards in the same row stretch to the row's height (grid's default
          align-items). The chart and the library take the cell classes
          themselves; the other two do not accept a className, so their cell
          is a one-item grid whose track stretches the card to fill it. */}
      <DocumentsAddedChart className="col-span-12 h-full wide:col-span-8" />

      <div className="col-span-12 grid h-full grid-cols-1 shell:col-span-6 wide:col-span-4">
        <AttentionList />
      </div>

      <LibraryBreakdown className="col-span-12 h-full wide:col-span-8" />

      <div className="col-span-12 grid h-full grid-cols-1 shell:col-span-6 wide:col-span-4">
        <AskCard />
      </div>

      <DashboardUploadDialog
        open={uploadOpen}
        onOpenChange={(v) => {
          setUploadOpen(v)
          if (!v) setDroppedFiles([])
        }}
        initialFiles={droppedFiles}
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/')({ component: DashboardPage })
