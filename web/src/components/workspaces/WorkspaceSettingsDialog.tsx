import { useNavigate } from '@tanstack/react-router'
import { Settings as SettingsIcon } from 'lucide-react'

import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription,
} from '@/components/ui/shadcn/dialog'
import { WorkspaceSettingsSections } from '@/routes/_authenticated/workspaces/$workspaceId/settings'

/**
 * Workspace settings as a modal — the browser header's single "Settings"
 * entry point. Renders the same sectioned body as the /settings route
 * (details, members, AI, storage, ownership, danger zone), so the AI
 * controls live here rather than as a separate button.
 */
export function WorkspaceSettingsDialog({
  open, onOpenChange, workspaceId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
}) {
  const navigate = useNavigate()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl gap-0 overflow-hidden p-0">
        <DialogHeader className="border-b border-border px-6 py-4 text-start">
          <DialogTitle className="flex items-center gap-2">
            <SettingsIcon className="h-5 w-5 text-muted-foreground" /> Workspace settings
          </DialogTitle>
          <DialogDescription>
            Details, members, AI, storage, ownership and danger zone.
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[calc(85vh-5.5rem)] overflow-y-auto px-6 py-5">
          {open && (
            <WorkspaceSettingsSections
              workspaceId={workspaceId}
              onDeleted={() => { onOpenChange(false); navigate({ to: '/workspaces' }) }}
            />
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
