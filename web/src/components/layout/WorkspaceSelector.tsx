import { useQuery } from '@tanstack/react-query'
import { getWorkspaces } from '@/api/workspaces'
import { useUIStore } from '@/store/uiStore'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'

export function WorkspaceSelector() {
  const { data: workspaces } = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })
  const { activeWorkspaceId, setActiveWorkspace } = useUIStore()
  const options = (workspaces || []).map((w) => ({ value: w.id, label: w.name }))
  return <Select value={activeWorkspaceId || ''} onValueChange={setActiveWorkspace} options={options} placeholder="Select workspace" />
}
