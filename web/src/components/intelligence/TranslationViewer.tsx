import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Download, X } from 'lucide-react'

import { getTranslationText } from '@/api/translation'
import { Button } from '@/components/ui/shadcn/button'
import { isRTL, languageLabel } from './LanguageBadge'

interface Props {
  translationId: string
  /** Optional: pass the source OCR text so the viewer can render it
   * on the left half. When omitted, only the translation is shown. */
  sourceText?: string
  onClose: () => void
}

export function TranslationViewer({ translationId, sourceText, onClose }: Props) {
  const { data, isLoading } = useQuery({
    queryKey: ['translation-text', translationId],
    queryFn: () => getTranslationText(translationId),
  })

  // ESC closes the modal.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const download = () => {
    if (!data?.translated_text) return
    const blob = new Blob([data.translated_text], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `translation-${data.target_language}.txt`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
    >
      <div
        className="flex h-[80vh] w-[min(1200px,95vw)] flex-col rounded-lg bg-white shadow-xl dark:bg-zinc-900"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-zinc-200 px-4 py-3 dark:border-zinc-800">
          <div className="flex items-center gap-3 text-sm">
            <span className="font-medium">Translation</span>
            {data && (
              <>
                <span className="text-zinc-500">
                  {languageLabel(data.source_language)} → {languageLabel(data.target_language)}
                </span>
                <span className="text-zinc-400">·</span>
                <span className="text-zinc-500">{data.word_count} words</span>
                {data.model_used && (
                  <>
                    <span className="text-zinc-400">·</span>
                    <span className="font-mono text-xs text-zinc-500">{data.model_used}</span>
                  </>
                )}
              </>
            )}
          </div>
          <div className="flex items-center gap-2">
            <Button size="sm" variant="outline" disabled={!data?.translated_text} onClick={download}>
              <Download className="me-1 h-4 w-4" />
              Download
            </Button>
            <Button size="sm" variant="ghost" onClick={onClose} aria-label="Close">
              <X className="h-4 w-4" />
            </Button>
          </div>
        </div>

        {isLoading && <div className="p-6 text-sm text-zinc-500">Loading…</div>}

        {data && (
          <div className="grid flex-1 grid-cols-1 gap-0 overflow-hidden md:grid-cols-2">
            {sourceText !== undefined && (
              <Pane label={`Source (${languageLabel(data.source_language)})`}
                    text={sourceText}
                    rtl={isRTL(data.source_language)} />
            )}
            <Pane
              label={`Translation (${languageLabel(data.target_language)})`}
              text={data.translated_text}
              rtl={isRTL(data.target_language)}
            />
          </div>
        )}
      </div>
    </div>
  )
}

function Pane({ label, text, rtl }: { label: string; text: string; rtl: boolean }) {
  return (
    <div className="flex flex-col overflow-hidden border-t border-zinc-200 first:border-t-0 dark:border-zinc-800 md:border-s md:border-t-0 md:first:border-s-0">
      <div className="border-b border-zinc-100 px-4 py-2 text-xs uppercase tracking-wide text-zinc-500 dark:border-zinc-900">
        {label}
      </div>
      <div
        dir={rtl ? 'rtl' : 'ltr'}
        className="flex-1 overflow-y-auto whitespace-pre-wrap p-4 text-sm leading-relaxed"
      >
        {text || <span className="text-zinc-400">(empty)</span>}
      </div>
    </div>
  )
}
