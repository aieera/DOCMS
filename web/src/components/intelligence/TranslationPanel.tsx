import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { AlertCircle, CheckCircle2, Download, Loader2, RotateCw } from 'lucide-react'

import {
  getDocumentLanguage,
  getTranslationText,
  listTranslations,
  requestTranslation,
  type Translation,
} from '@/api/translation'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { LabeledSelect } from '@/components/ui/shadcn/select'
import { formatRelativeTime } from '@/lib/formatters'
import { downloadTranslationText, translationFilename } from '@/lib/download'
import { TranslationViewer } from './TranslationViewer'
import { languageLabel } from './LanguageBadge'

interface Props {
  documentId: string
  versionId: string
  /** Passed through to the viewer so its left pane can render the original. */
  mimeType?: string
  title?: string
  /** Languages this tenant has enabled. Falls back to a default
   * set when the prop is omitted (matches server's DEFAULT_AVAILABLE). */
  availableLanguages?: string[]
}

const FALLBACK_LANGS = ['en', 'ar', 'fr', 'es', 'de', 'zh', 'ja', 'ko', 'hi', 'pt']

/**
 * Rail widget for language detection + translations. Renders as bare content:
 * the doc detail route wraps it in a `RailSection`, so the card, heading and
 * collapse behaviour come from the rail like every sibling panel.
 */
export function TranslationPanel({
  documentId,
  versionId,
  mimeType,
  title,
  availableLanguages,
}: Props) {
  const qc = useQueryClient()
  const langs = availableLanguages ?? FALLBACK_LANGS
  const [picked, setPicked] = useState<string | null>(null)
  const [target, setTarget] = useState<string | undefined>(undefined)

  const { data: lang } = useQuery({
    queryKey: ['document-language', documentId],
    queryFn: () => getDocumentLanguage(documentId),
  })

  const { data: translations } = useQuery({
    queryKey: ['translations', documentId],
    queryFn: () => listTranslations(documentId),
    refetchInterval: (q) => {
      const list = (q.state.data as Translation[] | undefined) ?? []
      return list.some((t) => t.status === 'pending' || t.status === 'processing')
        ? 5_000
        : false
    },
  })

  const ask = useAppMutation({
    mutationFn: (targetLanguage: string) =>
      requestTranslation({
        document_id: documentId,
        version_id: versionId,
        target_language: targetLanguage,
      }),
    onSuccess: (resp) => {
      qc.invalidateQueries({ queryKey: ['translations', documentId] })
      setTarget(undefined)
      toast.success(
        resp.deduplicated
          ? `Translation already exists (${languageLabel(resp.target_language)})`
          : `Translation queued (${languageLabel(resp.target_language)})`,
      )
    },
    onError: () => toast.error('Translation request failed'),
  })

  const existing = (translations ?? []).reduce<Record<string, Translation>>((acc, t) => {
    if (t.version_id === versionId) acc[t.target_language] = t
    return acc
  }, {})

  const remaining = langs.filter((l) => !(l in existing) && l !== lang?.detected_language)
  const rows = Object.values(existing)

  return (
    <div className="space-y-3" data-testid="translation-panel">
      <div className="text-sm">
        {lang?.detected_language ? (
          <span className="flex items-center gap-2">
            <span className="text-muted-foreground">Detected</span>
            <Badge variant="secondary">{languageLabel(lang.detected_language)}</Badge>
            {lang.confidence !== undefined && (
              <span className="text-xs text-muted-foreground">
                {/* QA SD-18: name a coin-flip for what it is. */}
                {lang.confidence < 0.75
                  ? `uncertain — ${Math.round(lang.confidence * 100)}% confidence`
                  : `${Math.round(lang.confidence * 100)}% confident`}
              </span>
            )}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">Language detection pending…</span>
        )}
      </div>

      {rows.length > 0 ? (
        <ul className="divide-y divide-border rounded-md border border-border">
          {rows.map((t) => (
            <TranslationRow
              key={t.id}
              t={t}
              docTitle={title}
              onView={() => setPicked(t.id)}
              onRetry={() => ask.mutate(t.target_language)}
              retrying={ask.isPending}
            />
          ))}
        </ul>
      ) : (
        <p className="text-xs text-muted-foreground">No translations yet.</p>
      )}

      <div className="flex items-end gap-2">
        <LabeledSelect
          className="min-w-0 flex-1"
          label={<span className="text-xs text-muted-foreground">Translate to</span>}
          placeholder={remaining.length === 0 ? 'All languages translated' : 'Choose language…'}
          value={target}
          onValueChange={setTarget}
          options={remaining.map((l) => ({ value: l, label: languageLabel(l) }))}
          disabled={ask.isPending || remaining.length === 0}
        />
        <Button
          size="sm"
          onClick={() => target && ask.mutate(target)}
          disabled={!target || ask.isPending}
        >
          {ask.isPending && <Loader2 className="me-1 h-3.5 w-3.5 animate-spin" />}
          Translate
        </Button>
      </div>

      {picked && (
        <TranslationViewer
          translationId={picked}
          documentId={documentId}
          versionId={versionId}
          mimeType={mimeType}
          title={title}
          onClose={() => setPicked(null)}
        />
      )}
    </div>
  )
}

function TranslationRow({
  t,
  docTitle,
  onView,
  onRetry,
  retrying,
}: {
  t: Translation
  docTitle?: string
  onView: () => void
  onRetry: () => void
  retrying: boolean
}) {
  const [downloading, setDownloading] = useState(false)

  // The list endpoint carries metadata only, so the text is fetched on demand
  // rather than eagerly for every row.
  const download = async () => {
    setDownloading(true)
    try {
      const full = await getTranslationText(t.id)
      downloadTranslationText(
        full.translated_text,
        translationFilename(docTitle, t.target_language),
      )
    } catch {
      toast.error('Could not download the translation')
    } finally {
      setDownloading(false)
    }
  }

  return (
    <li className="flex flex-wrap items-center gap-x-2 gap-y-1 px-2.5 py-2 text-sm">
      <span className="min-w-0 flex-1 truncate font-medium">
        {languageLabel(t.target_language)}
      </span>
      <StatusPill status={t.status} />

      <div className="flex w-full items-center gap-2 text-xs text-muted-foreground">
        {t.status === 'completed' && <span>{t.word_count} words</span>}
        {t.status === 'failed' && t.error_message && (
          <span className="min-w-0 truncate text-destructive" title={t.error_message}>
            {t.error_message}
          </span>
        )}
        <span className="whitespace-nowrap">
          {formatRelativeTime(t.completed_at ?? t.created_at)}
        </span>

        <span className="flex-1" />

        {t.status === 'completed' && (
          <>
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2"
              onClick={download}
              disabled={downloading}
              aria-label={`Download ${languageLabel(t.target_language)} translation`}
            >
              {downloading ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Download className="h-3.5 w-3.5" />
              )}
            </Button>
            <Button size="sm" variant="outline" className="h-7 px-2" onClick={onView}>
              View
            </Button>
          </>
        )}

        {t.status === 'failed' && (
          <Button
            size="sm"
            variant="outline"
            className="h-7 px-2"
            onClick={onRetry}
            disabled={retrying}
          >
            <RotateCw className="me-1 h-3.5 w-3.5" />
            Retry
          </Button>
        )}
      </div>
    </li>
  )
}

function StatusPill({ status }: { status: Translation['status'] }) {
  if (status === 'completed') {
    return (
      <span className="inline-flex shrink-0 items-center gap-1 text-xs text-emerald-600 dark:text-emerald-400">
        <CheckCircle2 className="h-3 w-3" /> Completed
      </span>
    )
  }
  if (status === 'failed') {
    return (
      <span className="inline-flex shrink-0 items-center gap-1 text-xs text-destructive">
        <AlertCircle className="h-3 w-3" /> Failed
      </span>
    )
  }
  return (
    <span className="inline-flex shrink-0 items-center gap-1 text-xs text-amber-600 dark:text-amber-400">
      <Loader2 className="h-3 w-3 animate-spin" />
      {status === 'processing' ? 'Processing…' : 'Pending'}
    </span>
  )
}
