import { useQuery } from '@tanstack/react-query'
import { Gauge } from 'lucide-react'

import { getDocumentOcrQuality, type QualityGrade } from '@/api/ocr-quality'
import { Badge } from '@/components/ui/Badge'

interface Props {
  documentId: string
  inline?: { grade: QualityGrade; pages_needing_review?: number }
}

const VARIANT: Record<QualityGrade, string> = {
  excellent: 'active',     // green
  good:      'superseded', // blue
  fair:      'in_review',  // amber
  poor:      'disposed',   // red
}

const LABEL: Record<QualityGrade, string> = {
  excellent: 'Excellent',
  good:      'Good',
  fair:      'Fair',
  poor:      'Poor',
}

export function OcrQualityBadge({ documentId, inline }: Props) {
  const enabled = !inline
  const { data } = useQuery({
    queryKey: ['ocr-quality', documentId],
    queryFn: () => getDocumentOcrQuality(documentId),
    enabled,
  })
  const grade = inline?.grade ?? data?.summary?.quality_grade
  const needs = inline?.pages_needing_review ?? data?.summary?.pages_needing_review ?? 0
  if (!grade) return null
  const tooltip = needs > 0 ? `${needs} page${needs === 1 ? '' : 's'} need review` : 'No pages need review'
  return (
    <Badge variant={VARIANT[grade]} className="gap-1">
      <Gauge className="h-3 w-3" />
      <span title={tooltip}>OCR: {LABEL[grade]}</span>
    </Badge>
  )
}
