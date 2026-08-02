import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Globe, Loader2, AlertCircle, CheckCircle2 } from 'lucide-react'

import {
  getDocumentLanguage,
  listTranslations,
  requestTranslation,
  type Translation,
} from '@/api/translation'
import { Button } from '@/components/ui/shadcn/button'
import { TranslationViewer } from './TranslationViewer'
import { languageLabel } from './LanguageBadge'

interface Props {
  documentId: string
  versionId: string
  /** Languages this tenant has enabled. Falls back to a default
   * set when the prop is omitted (matches server's DEFAULT_AVAILABLE). */
  availableLanguages?: string[]
}

const FALLBACK_LANGS = ['en', 'ar', 'fr', 'es', 'de', 'zh', 'ja', 'ko', 'hi', 'pt']

export function TranslationPanel({ documentId, versionId, availableLanguages }: Props) {
  const qc = useQueryClient()
  const langs = availableLanguages ?? FALLBACK_LANGS
  const [picked, setPicked] = useState<string | null>(null)

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
    mutationFn: (target: string) =>
      requestTranslation({
        document_id: documentId,
        version_id: versionId,
        target_language: target,
      }),
    onSuccess: (resp) => {
      qc.invalidateQueries({ queryKey: ['translations', documentId] })
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

  return (
    <div className="rounded border border-zinc-200 dark:border-zinc-800">
      <div className="flex items-center gap-2 border-b border-zinc-100 px-4 py-2 text-sm font-medium dark:border-zinc-900">
        <Globe className="h-4 w-4 text-violet-500" />
        Language &amp; translation
      </div>

      <div className="px-4 py-3 text-sm">
        {lang?.detected_language ? (
          <div>
            Detected: <span className="font-medium">{languageLabel(lang.detected_language)}</span>
            {lang.confidence !== undefined && (
              <span className="ms-2 text-zinc-600">({Math.round(lang.confidence * 100)}%)</span>
            )}
          </div>
        ) : (
          <div className="text-zinc-600">Language detection pending…</div>
        )}
      </div>

      <ul className="divide-y divide-zinc-100 dark:divide-zinc-900">
        {Object.values(existing).map((t) => (
          <TranslationRow
            key={t.id}
            t={t}
            onView={() => setPicked(t.id)}
          />
        ))}
        {Object.keys(existing).length === 0 && (
          <li className="px-4 py-3 text-sm text-zinc-600">No translations yet.</li>
        )}
      </ul>

      <div className="flex items-center gap-2 border-t border-zinc-100 px-4 py-3 text-sm dark:border-zinc-900">
        <span className="text-zinc-600">Translate to:</span>
        <select
          aria-label="Translate to language"
          className="rounded border border-zinc-200 bg-white px-2 py-1 text-sm dark:border-zinc-800 dark:bg-zinc-900"
          defaultValue=""
          onChange={(e) => {
            const v = e.target.value
            if (!v) return
            ask.mutate(v)
            e.currentTarget.value = ''
          }}
          disabled={ask.isPending || remaining.length === 0}
        >
          <option value="" disabled>
            {remaining.length === 0 ? 'All languages translated' : 'Choose…'}
          </option>
          {remaining.map((l) => (
            <option key={l} value={l}>
              {languageLabel(l)}
            </option>
          ))}
        </select>
      </div>

      {picked && (
        <TranslationViewer
          translationId={picked}
          onClose={() => setPicked(null)}
        />
      )}
    </div>
  )
}

function TranslationRow({ t, onView }: { t: Translation; onView: () => void }) {
  const label = languageLabel(t.target_language)
  return (
    <li className="flex items-center gap-3 px-4 py-2 text-sm">
      <span className="w-32 truncate">{label}</span>
      <StatusPill status={t.status} />
      {t.status === 'completed' && (
        <span className="text-xs text-zinc-600">{t.word_count} words</span>
      )}
      {t.status === 'failed' && t.error_message && (
        <span className="truncate text-xs text-red-500" title={t.error_message}>
          {t.error_message}
        </span>
      )}
      <span className="flex-1" />
      {t.status === 'completed' && (
        <Button size="sm" variant="outline" onClick={onView}>
          View
        </Button>
      )}
    </li>
  )
}

function StatusPill({ status }: { status: Translation['status'] }) {
  if (status === 'completed') {
    return (
      <span className="inline-flex items-center gap-1 text-xs text-emerald-600">
        <CheckCircle2 className="h-3 w-3" /> Completed
      </span>
    )
  }
  if (status === 'failed') {
    return (
      <span className="inline-flex items-center gap-1 text-xs text-red-600">
        <AlertCircle className="h-3 w-3" /> Failed
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1 text-xs text-amber-600">
      <Loader2 className="h-3 w-3 animate-spin" />
      {status === 'processing' ? 'Processing…' : 'Pending'}
    </span>
  )
}
