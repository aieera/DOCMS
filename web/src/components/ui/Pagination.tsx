import { Button } from './Button'
import { ChevronLeft, ChevronRight } from 'lucide-react'

interface PaginationProps {
  pageToken?: string
  hasMore: boolean
  onNext: () => void
  onPrev: () => void
  hasPrev: boolean
  total?: number
}

export function Pagination({ hasMore, onNext, onPrev, hasPrev, total }: PaginationProps) {
  return (
    <div className="flex items-center justify-between pt-4">
      <span className="text-sm text-[var(--color-text-secondary)]">{total != null ? `${total} total` : ''}</span>
      <div className="flex gap-1">
        <Button variant="outline" size="sm" disabled={!hasPrev} onClick={onPrev}><ChevronLeft className="h-4 w-4" /></Button>
        <Button variant="outline" size="sm" disabled={!hasMore} onClick={onNext}><ChevronRight className="h-4 w-4" /></Button>
      </div>
    </div>
  )
}
