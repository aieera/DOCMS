import { copyText } from '@/lib/clipboard'
import { useQuery } from '@tanstack/react-query'
import { VisuallyHidden } from '@radix-ui/react-visually-hidden'
import { AlertCircle, Check, Copy, Download, Loader2, X } from 'lucide-react'
import { useState } from 'react'

import { getTranslationText } from '@/api/translation'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { FileIcon } from '@/components/ui/FileIcon'
import {
  Dialog as DialogRoot,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from '@/components/ui/shadcn/dialog'
import { DocumentPreview } from '@/components/viewer/DocumentPreview'
import { cn } from '@/lib/cn'
import { downloadTranslationText, translationFilename } from '@/lib/download'
import { isRTL, languageLabel } from './LanguageBadge'

interface Props {
  translationId: string
  /** Identifies the version whose rendered original fills the left pane. */
  documentId: string
  versionId: string
  mimeType?: string
  /** Document title — header label and the download filename stem. */
  title?: string
  onClose: () => void
}

/**
 * Side-by-side translation surface: the rendered original on the left, the
 * translated text on the right.
 *
 * Shares its chrome with `DocumentViewerModal` (same Radix dialog, same
 * 92vh/94vw geometry, same custom header bar) so opening a translation feels
 * like opening the document. The left pane is literally `<DocumentPreview>` —
 * the component the doc detail page's preview tab renders — so PDFs, images
 * and text bodies all behave exactly as they do there.
 */
export function TranslationViewer({
  translationId,
  documentId,
  versionId,
  mimeType,
  title,
  onClose,
}: Props) {
  const { data, isLoading, isError } = useQuery({
    queryKey: ['translation-text', translationId],
    queryFn: () => getTranslationText(translationId),
  })
  const [copied, setCopied] = useState(false)

  const text = data?.translated_text ?? ''
  const targetLang = data?.target_language ?? ''
  const rtl = isRTL(targetLang)

  const copy = async () => {
    try {
      await copyText(text)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      /* clipboard denied (insecure origin / permissions) — no-op */
    }
  }

  return (
    <DialogRoot open onOpenChange={(next) => !next && onClose()}>
      <DialogContent
        hideCloseButton
        className="flex h-[92vh] max-w-[94vw] flex-col gap-0 overflow-hidden rounded-xl p-0 sm:max-w-[94vw]"
        data-testid="translation-viewer"
      >
        {/* Radix needs a title + description even when the visible header is
            bespoke; keep them for AT only. */}
        <VisuallyHidden>
          <DialogTitle>
            {title ? `${title} — translation` : 'Translation'}
          </DialogTitle>
          <DialogDescription>
            The original document alongside its translated text.
          </DialogDescription>
        </VisuallyHidden>

        <header className="flex shrink-0 items-center gap-3 border-b border-border bg-card/60 px-5 py-3 backdrop-blur">
          <FileIcon mime={mimeType} className="h-6 w-6 shrink-0" />

          <div className="flex min-w-0 flex-1 flex-col">
            <div className="flex min-w-0 items-center gap-2">
              <h2 className="truncate text-sm font-semibold leading-none">
                {title ?? 'Translation'}
              </h2>
              {data && (
                <Badge variant="secondary" className="shrink-0 gap-1 font-normal">
                  {languageLabel(data.source_language)}
                  <span aria-hidden>→</span>
                  <span className="font-medium">{languageLabel(data.target_language)}</span>
                </Badge>
              )}
            </div>
            {data && (
              <p className="mt-1 truncate text-xs text-muted-foreground">
                {data.word_count} words
                {data.model_used && (
                  <>
                    <span className="mx-1.5" aria-hidden>·</span>
                    <code className="rounded bg-muted px-1 py-0.5 font-mono text-[10px]">
                      {data.model_used}
                    </code>
                  </>
                )}
              </p>
            )}
          </div>

          <div className="flex shrink-0 items-center gap-1">
            <Button
              size="sm"
              variant="ghost"
              disabled={!text}
              onClick={copy}
              aria-label="Copy translation"
            >
              {copied ? <Check className="me-1 h-4 w-4" /> : <Copy className="me-1 h-4 w-4" />}
              {copied ? 'Copied' : 'Copy'}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={!text}
              onClick={() => downloadTranslationText(text, translationFilename(title, targetLang))}
            >
              <Download className="me-1 h-4 w-4" />
              Download
            </Button>
            <Button
              variant="ghost"
              size="icon"
              onClick={onClose}
              aria-label="Close translation viewer"
              data-testid="translation-viewer-close"
            >
              <X className="h-4 w-4" />
            </Button>
          </div>
        </header>

        {/* Panes stack below md — two 380px columns would make both unreadable. */}
        <div className="grid min-h-0 flex-1 grid-cols-1 overflow-hidden md:grid-cols-2">
          <Pane label={`Original${data ? ` (${languageLabel(data.source_language)})` : ''}`}>
            <DocumentPreview
              documentId={documentId}
              versionId={versionId}
              mimeType={mimeType}
              title={title}
            />
          </Pane>

          <Pane
            label={`Translation${data ? ` (${languageLabel(data.target_language)})` : ''}`}
            className="border-t border-border md:border-s md:border-t-0"
            testId="translation-pane"
          >
            {isLoading && (
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                Loading translation…
              </div>
            )}

            {isError && <Failure message="The translation could not be loaded." />}

            {data?.status === 'failed' && (
              <Failure message={data.error_message || 'The translation job failed.'} />
            )}

            {data && data.status !== 'failed' && data.status !== 'completed' && (
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                Translation is still {data.status === 'processing' ? 'processing' : 'queued'}…
              </div>
            )}

            {data?.status === 'completed' &&
              (text ? (
                <article
                  dir={rtl ? 'rtl' : 'ltr'}
                  lang={targetLang || undefined}
                  data-testid="translation-text"
                  className={cn(
                    'mx-auto max-w-prose whitespace-pre-wrap rounded-xl bg-muted p-6 text-start shadow-neu-inset',
                    // Arabic/Hebrew/Persian glyphs render visually smaller and
                    // need more leading than Latin at the same px.
                    rtl ? 'text-[15px] leading-loose' : 'text-sm leading-relaxed',
                  )}
                >
                  {text}
                </article>
              ) : (
                <p className="text-sm text-muted-foreground">
                  The translation completed but returned no text.
                </p>
              ))}
          </Pane>
        </div>
      </DialogContent>
    </DialogRoot>
  )
}

function Pane({
  label,
  className,
  testId,
  children,
}: {
  label: string
  className?: string
  testId?: string
  children: React.ReactNode
}) {
  return (
    <section className={cn('flex min-h-0 flex-col overflow-hidden', className)} data-testid={testId}>
      <h3 className="shrink-0 border-b border-border px-4 py-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        {label}
      </h3>
      <div className="min-h-0 flex-1 overflow-y-auto bg-background p-4">{children}</div>
    </section>
  )
}

function Failure({ message }: { message: string }) {
  return (
    <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-foreground">
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
      <span className="min-w-0 break-words">{message}</span>
    </div>
  )
}
