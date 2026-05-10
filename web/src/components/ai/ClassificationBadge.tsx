import { Badge } from '@/components/ui/shadcn/badge'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/shadcn/tooltip'
import { Sparkles } from 'lucide-react'

export function ClassificationBadge({ documentClass, confidence }: { documentClass?: string; confidence?: number }) {
  if (!documentClass) return null
  const pct = confidence != null ? `${Math.round(confidence * 100)}%` : ''
  return (
    <TooltipProvider delayDuration={200}>
      <Tooltip>
        <TooltipTrigger asChild>
          <span>
            <Badge className="gap-1">
              <Sparkles className="h-3 w-3" />
              {documentClass}
            </Badge>
          </span>
        </TooltipTrigger>
        <TooltipContent>AI classified as {documentClass}{pct && ` (${pct} confidence)`}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
