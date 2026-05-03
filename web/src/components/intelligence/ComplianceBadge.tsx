import { useQuery } from '@tanstack/react-query'
import { ShieldAlert } from 'lucide-react'

import { getDocumentCompliance, type RiskLevel } from '@/api/compliance-pii'
import { Badge } from '@/components/ui/Badge'

interface Props {
  documentId: string
  /** When true, fetches per-doc compliance. When the parent already
   * has the summary inline (e.g. doc list), pass overall_risk + counts
   * directly to skip the extra request. */
  inline?: { overall_risk: RiskLevel; entity_types_found?: string[] }
}

const VARIANT: Record<RiskLevel, string> = {
  critical: 'disposed',   // red
  high:     'in_review',  // amber
  medium:   'in_review',  // amber
  low:      'active',     // green
  none:     'archived',   // grey (rendered only if forced)
}

const LABEL: Record<RiskLevel, string> = {
  critical: 'Critical',
  high:     'High risk',
  medium:   'Medium risk',
  low:      'Low risk',
  none:     'Clean',
}

export function ComplianceBadge({ documentId, inline }: Props) {
  const enabled = !inline
  const { data } = useQuery({
    queryKey: ['compliance', documentId],
    queryFn: () => getDocumentCompliance(documentId),
    enabled,
    refetchInterval: 30_000,
  })

  const risk: RiskLevel = inline?.overall_risk ?? data?.summary?.overall_risk ?? 'none'
  const types = inline?.entity_types_found ?? data?.summary?.entity_types_found ?? []
  if (risk === 'none') return null

  const tooltip = types.length > 0 ? `Contains: ${types.slice(0, 5).join(', ')}` : undefined

  return (
    <Badge variant={VARIANT[risk]} className="gap-1" >
      <ShieldAlert className="h-3 w-3" />
      <span title={tooltip}>{LABEL[risk]}</span>
    </Badge>
  )
}
