import { useQuery } from '@tanstack/react-query'
import { AlertTriangle } from 'lucide-react'
import { getWorkspaces } from '@/api/workspaces'
import { useUIStore } from '@/store/uiStore'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'

export function WorkspaceSelector() {
  const { data: workspaces, isError } = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })
  const { activeWorkspaceId, setActiveWorkspace } = useUIStore()
  // Wave 5 pattern 3: getWorkspaces throws UnknownListShapeError on a
  // malformed response (was silently returning []). Surface it instead
  // of rendering an empty picker that looks like "you have no
  // workspaces" — cf. VersionHistory's isError branch.
  if (isError) {
    return (
      <span className="flex items-center gap-1.5 text-xs text-destructive" role="alert">
        <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden />
        Couldn&apos;t load workspaces
      </span>
    )
  }
  const options = (workspaces || []).map((w) => ({ value: w.id, label: w.name }))
  return <Select value={activeWorkspaceId || ''} onValueChange={setActiveWorkspace} options={options} placeholder="Select workspace" />
}
