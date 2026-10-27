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
 * it hands the question to the Ask page in router history state, which the
 * page asks once on arrival, so the answer streams in the full chat view with
 * its history and scope selector. Not `/ask?q=`: a URL can be crafted by
 * anyone, so the Ask page only ever prefills from `q` and never sends it.
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
    void navigate({ to: '/ask', state: { askQuestion: question } })
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== 'Enter' || e.shiftKey) return
    // Enter is also how a composing input method (CJK, Indic and
    // transliteration keyboards) accepts its candidate text, and that Enter
    // must not send. Chromium and Firefox flag it with `isComposing`. Safari
    // ends the composition before `keydown`, so there `isComposing` is already
    // false and the event reports `keyCode` 229 instead.
    if (e.nativeEvent.isComposing || e.keyCode === 229) return
    e.preventDefault()
    submit()
  }

  return (
    <WidgetCard
      title={t('dashboard.ask.title')}
      subtitle={t('dashboard.ask.subtitle')}
      icon={
        <span
          data-testid="ask-card-icon"
          className="flex h-8 w-8 items-center justify-center rounded-md bg-muted text-foreground"
          aria-hidden
        >
          <Sparkles className="h-4 w-4" />
        </span>
      }
    >
      <div className="flex flex-1 flex-col gap-3">
        <div className="space-y-1.5">
          <Label htmlFor={inputId}>{t('dashboard.ask.label')}</Label>
          <Textarea
            id={inputId}
            dir="auto"
            rows={3}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKeyDown}
            maxLength={MAX_QUESTION_LEN}
            placeholder={t('dashboard.ask.placeholder')}
            aria-describedby={hintId}
            className="resize-none"
          />
        </div>
        {/* mt-auto: when the grid row stretches the card, the footer stays at its bottom. */}
        <div className="mt-auto flex items-center justify-between gap-3">
          <p id={hintId} className="min-w-0 text-caption text-muted-foreground">{t('dashboard.ask.hint')}</p>
          <Button type="button" onClick={submit} disabled={!question} className="shrink-0">
            {t('dashboard.ask.submit')}
          </Button>
        </div>
      </div>
    </WidgetCard>
  )
}
