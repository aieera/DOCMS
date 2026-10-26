import { useId, useState, type KeyboardEvent } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { Sparkles } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/shadcn/button'
import { Textarea } from '@/components/ui/shadcn/textarea'
import { Label } from '@/components/ui/label'
import { MAX_QUESTION_LEN } from '@/lib/askLimits'
import { WidgetCard } from './WidgetCard'

/**
 * The dashboard's "Ask a question" card. It does not answer anything itself:
 * it hands the question to the Ask page as `/ask?q=<text>`, which submits it
 * on arrival, so the answer streams in the full chat view with its history
 * and scope selector.
 */
export function AskCard() {
  const { t } = useTranslation('common')
  const navigate = useNavigate()
  const [text, setText] = useState('')
  const inputId = useId()
  const hintId = useId()
  const question = text.trim()

  const submit = () => {
    if (!question) return
    void navigate({ to: '/ask', search: { q: question } })
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // Shift+Enter is a newline. The Enter that commits an IME composition
    // (Arabic, Chinese, Japanese and Korean input methods) is not "send":
    // `isComposing` is true for it.
    if (e.key !== 'Enter' || e.shiftKey || e.nativeEvent.isComposing) return
    e.preventDefault()
    submit()
  }

  return (
    <WidgetCard
      title={t('dashboard.ask.title')}
      subtitle={t('dashboard.ask.subtitle')}
      // WidgetCard has no slot at the start of its header, so the icon tile
      // rides in the action slot, at the end edge (it mirrors under RTL).
      action={
        <span
          className="flex h-8 w-8 items-center justify-center rounded-md bg-muted text-foreground"
          aria-hidden
        >
          <Sparkles className="h-4 w-4" />
        </span>
      }
    >
      <div className="flex flex-1 flex-col gap-3">
        {/* The card heading already says "Ask a question"; repeating it as a
            visible label would be noise, so the label is for assistive tech. */}
        <Label htmlFor={inputId} className="sr-only">{t('dashboard.ask.title')}</Label>
        <Textarea
          id={inputId}
          rows={3}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          maxLength={MAX_QUESTION_LEN}
          placeholder={t('dashboard.ask.placeholder')}
          aria-describedby={hintId}
          className="resize-none"
        />
        <div className="flex items-center justify-between gap-3">
          <p id={hintId} className="min-w-0 text-caption text-muted-foreground">{t('dashboard.ask.hint')}</p>
          <Button type="button" onClick={submit} disabled={!question} className="shrink-0">
            {t('dashboard.ask.submit')}
          </Button>
        </div>
      </div>
    </WidgetCard>
  )
}
