import { ShieldAlert, ShieldCheck, Lock } from 'lucide-react'

import type { Document } from '@/types/api'

// SensitivityBadge renders a document's §8 security classification plus PHI/PII
// markers. Compact inline pills; renders nothing when the document carries no
// sensitivity signal at all.

const LEVEL_STYLE: Record<string, string> = {
  restricted: 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-200',
  confidential: 'bg-warning/15 text-warning-strong',
  internal: 'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-200',
  unclassified: 'bg-muted text-muted-foreground',
}

export function SensitivityBadge({ doc, className }: { doc: Pick<Document, 'security_classification' | 'has_phi' | 'has_pii'>; className?: string }) {
  const level = doc.security_classification || ''
  const hasPHI = !!doc.has_phi
  const hasPII = !!doc.has_pii
  if (!level && !hasPHI && !hasPII) return null

  const Icon = level === 'restricted' ? Lock : level === 'confidential' ? ShieldAlert : ShieldCheck

  return (
    <span className={`inline-flex flex-wrap items-center gap-1 ${className ?? ''}`}>
      {level && (
        <span
          className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium capitalize ${
            LEVEL_STYLE[level] ?? LEVEL_STYLE.unclassified
          }`}
        >
          <Icon className="h-3 w-3" />
          {level}
        </span>
      )}
      {hasPHI && (
        <span className="inline-flex items-center rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-800 dark:bg-red-900/40 dark:text-red-200">
          PHI
        </span>
      )}
      {hasPII && (
        <span className="inline-flex items-center rounded-full bg-warning/15 px-2 py-0.5 text-xs font-medium text-warning-strong">
          PII
        </span>
      )}
    </span>
  )
}
