// DocumentHeaderToolbar — the compact action row for the document
// viewer header. Replaces the old side-rail button stack (Download /
// Share / Create task / Compare / Manage access as five full-width
// buttons) and the standalone integrity band, reclaiming the rail for
// content (tags, details, comments) and the header for the preview.
//
// Pure presentational: every action is a callback prop; the route owns
// the logic (presigned download URLs, dialogs, mutations).
import {
  Archive,
  CheckSquare,
  Download,
  GitCompareArrows,
  Lock,
  MoreHorizontal,
  Share2,
  ShieldCheck,
  UserCog,
} from 'lucide-react'

import { Button } from '@/components/ui/shadcn/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/shadcn/dropdown-menu'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/shadcn/tooltip'

export interface DocumentHeaderToolbarProps {
  onDownload: () => void
  onShare: () => void
  onCreateTask: () => void
  onCompare: () => void
  onManageAccess: () => void
  // Secondary (rare) actions are optional: callers that keep the
  // full-status records/integrity components elsewhere simply omit
  // these and no overflow menu renders.
  onDeclareRecord?: () => void
  onWormLock?: () => void
  onVerifyIntegrity?: () => void
  canDeclareRecord?: boolean
  // False when there is no uploaded version yet.
  canDownload?: boolean
}

const PRIMARY = [
  { key: 'download', label: 'Download', icon: Download, cb: 'onDownload' },
  { key: 'share', label: 'Share', icon: Share2, cb: 'onShare' },
  { key: 'create-task', label: 'Create task', icon: CheckSquare, cb: 'onCreateTask' },
  { key: 'compare', label: 'Compare with…', icon: GitCompareArrows, cb: 'onCompare' },
  { key: 'manage-access', label: 'Manage access', icon: UserCog, cb: 'onManageAccess' },
] as const

export function DocumentHeaderToolbar(props: DocumentHeaderToolbarProps) {
  const hasOverflow = Boolean(props.onDeclareRecord || props.onWormLock || props.onVerifyIntegrity)
  return (
    <TooltipProvider delayDuration={300}>
      <div className="flex items-center gap-1" data-testid="document-header-toolbar">
        {PRIMARY.map(({ key, label, icon: Icon, cb }) => (
          <Tooltip key={key}>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="sm"
                className="h-9 w-9 p-0"
                aria-label={label}
                disabled={key === 'download' && props.canDownload === false}
                onClick={props[cb]}
              >
                <Icon className="h-4 w-4" aria-hidden />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="bottom">{label}</TooltipContent>
          </Tooltip>
        ))}

        {hasOverflow && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="sm" className="h-9 w-9 p-0" aria-label="More actions">
                <MoreHorizontal className="h-4 w-4" aria-hidden />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-52">
              {props.canDeclareRecord && props.onDeclareRecord && (
                <DropdownMenuItem onClick={props.onDeclareRecord}>
                  <Archive className="me-2 h-4 w-4" aria-hidden /> Declare as record
                </DropdownMenuItem>
              )}
              {props.onWormLock && (
                <DropdownMenuItem onClick={props.onWormLock}>
                  <Lock className="me-2 h-4 w-4" aria-hidden /> WORM lock
                </DropdownMenuItem>
              )}
              {props.onVerifyIntegrity && (
                <DropdownMenuItem onClick={props.onVerifyIntegrity}>
                  <ShieldCheck className="me-2 h-4 w-4" aria-hidden /> Verify integrity
                </DropdownMenuItem>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
    </TooltipProvider>
  )
}
