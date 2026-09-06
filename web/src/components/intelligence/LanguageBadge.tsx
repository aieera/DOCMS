import { useQuery } from '@tanstack/react-query'
import { Globe } from 'lucide-react'

import { getDocumentLanguage } from '@/api/translation'
import { Badge } from '@/components/ui/shadcn/badge'

interface Props {
  documentId: string
}

const LABELS: Record<string, string> = {
  en: 'English', ar: 'Arabic', fr: 'French', es: 'Spanish', de: 'German',
  zh: 'Chinese', ja: 'Japanese', ko: 'Korean', hi: 'Hindi', pt: 'Portuguese',
  ru: 'Russian', it: 'Italian', nl: 'Dutch', tr: 'Turkish', he: 'Hebrew',
  fa: 'Persian', ur: 'Urdu', vi: 'Vietnamese', th: 'Thai', id: 'Indonesian',
}

export function LanguageBadge({ documentId }: Props) {
  const { data } = useQuery({
    queryKey: ['document-language', documentId],
    queryFn: () => getDocumentLanguage(documentId),
  })
  if (!data?.detected_language) return null
  // QA SD-18: an English PDF wore an "ET (50%)" badge beside its title —
  // a coin-flip detection is not worth promoting to a header badge. The
  // Language & translation panel still shows low-confidence detections,
  // labelled as uncertain; the badge only appears once it's credible.
  if (data.confidence != null && data.confidence < 0.75) return null
  const code = data.detected_language
  const label = LABELS[code] ?? code.toUpperCase()
  const conf = data.confidence ? Math.round(data.confidence * 100) : null
  return (
    <Badge variant="default" className="gap-1">
      <Globe className="h-3 w-3" />
      <span>{label}{conf !== null ? ` (${conf}%)` : ''}</span>
    </Badge>
  )
}

export function languageLabel(code: string): string {
  return LABELS[code] ?? code.toUpperCase()
}

export function isRTL(code: string): boolean {
  return ['ar', 'he', 'fa', 'ur'].includes(code)
}
