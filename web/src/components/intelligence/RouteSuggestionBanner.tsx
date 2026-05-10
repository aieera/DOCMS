import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ChevronDown, ChevronUp, FolderTree, X } from 'lucide-react'

import {
  acceptRouteSuggestion,
  dismissRouteSuggestion,
  listRouteSuggestions,
  type RouteSuggestion,
} from '@/api/smart-routing'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'

interface Props {
  documentId: string
}

const SOURCE_LABEL: Record<RouteSuggestion['match_source'], string> = {
  rule: 'rule',
  history: 'pattern',
  similarity: 'similar docs',
}

export function RouteSuggestionBanner({ documentId }: Props) {
  const qc = useQueryClient()
  const [expanded, setExpanded] = useState(false)

  const { data, isLoading } = useQuery({
    queryKey: ['route-suggestions', documentId],
    queryFn: () => listRouteSuggestions(documentId),
    refetchInterval: 15_000,
  })

  const accept = useMutation({
    mutationFn: (sid: string) => acceptRouteSuggestion(documentId, sid),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['route-suggestions', documentId] })
      qc.invalidateQueries({ queryKey: ['document', documentId] })
      toast.success('Document moved')
    },
    onError: () => toast.error('Move failed'),
  })

  const dismiss = useMutation({
    mutationFn: (sid: string) => dismissRouteSuggestion(documentId, sid),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ['route-suggestions', documentId] }),
    onError: () => toast.error('Dismiss failed'),
  })

  if (isLoading) return null
  const pending = (data?.suggestions ?? []).filter((s) => s.status === 'pending')
  if (pending.length === 0) return null

  const top = pending[0]
  const rest = pending.slice(1)

  return (
    <div className="rounded border border-violet-200 bg-violet-50 p-4 text-sm dark:border-violet-900 dark:bg-violet-950/40">
      <div className="flex items-center gap-3">
        <FolderTree className="h-5 w-5 text-violet-600" />
        <div className="flex-1">
          <span className="font-medium">Suggested location: </span>
          <span>{top.folder_path}</span>
          <span className="ml-2 text-violet-600 tabular-nums">
            {Math.round(top.confidence * 100)}%
          </span>
          <span className="ml-2 text-xs text-zinc-500">
            via {SOURCE_LABEL[top.match_source]}
          </span>
        </div>
        <Button
          size="sm"
          disabled={accept.isPending}
          onClick={() => accept.mutate(top.id)}
        >
          Move here
        </Button>
        {rest.length > 0 && (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => setExpanded((v) => !v)}
            aria-expanded={expanded}
            aria-label={`See ${rest.length} more suggestion${rest.length === 1 ? '' : 's'}`}
          >
            {expanded ? <ChevronUp className="h-4 w-4" /> : <ChevronDown className="h-4 w-4" />}
            <span className="ml-1">+{rest.length}</span>
          </Button>
        )}
        <Button
          size="sm"
          variant="ghost"
          aria-label="Dismiss"
          disabled={dismiss.isPending}
          onClick={() => dismiss.mutate(top.id)}
        >
          <X className="h-4 w-4" />
        </Button>
      </div>

      {expanded && rest.length > 0 && (
        <ul className="mt-3 space-y-1.5 border-t border-violet-200 pt-3 dark:border-violet-900">
          {rest.map((s) => (
            <li key={s.id} className="flex items-center gap-3">
              <span className="flex-1 truncate" title={s.folder_path}>
                {s.folder_path}
              </span>
              <span className="tabular-nums text-zinc-500">{Math.round(s.confidence * 100)}%</span>
              <Badge variant="outline" className="text-[10px]">
                {SOURCE_LABEL[s.match_source]}
              </Badge>
              <Button
                size="sm"
                variant="outline"
                disabled={accept.isPending}
                onClick={() => accept.mutate(s.id)}
              >
                Move here
              </Button>
              <Button
                size="sm"
                variant="ghost"
                aria-label={`Dismiss ${s.folder_path}`}
                disabled={dismiss.isPending}
                onClick={() => dismiss.mutate(s.id)}
              >
                <X className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
