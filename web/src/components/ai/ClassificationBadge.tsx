import { Badge } from '@/components/ui/Badge'
import { Tooltip } from '@/components/ui/Tooltip'
import { Sparkles } from 'lucide-react'

export function ClassificationBadge({ documentClass, confidence }: { documentClass?: string; confidence?: number }) {
  if (!documentClass) return null
  const pct = confidence != null ? `${Math.round(confidence * 100)}%` : ''
  return (
    <Tooltip content={`AI classified as ${documentClass} (${pct} confidence)`}>
      <span>
        <Badge className="gap-1">
          <Sparkles className="h-3 w-3" />
          {documentClass}
        </Badge>
      </span>
    </Tooltip>
  )
}
