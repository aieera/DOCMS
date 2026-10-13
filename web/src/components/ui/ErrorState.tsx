import { AlertTriangle } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from './shadcn/button'
import { cn } from '@/lib/cn'

interface ErrorStateProps {
  message?: string
  onRetry?: () => void
  /** 'default' (full-page) or 'sm' (dashboard-widget scale). Defaults to 'default' so every existing call site is unaffected. */
  size?: 'sm' | 'default'
}

export function ErrorState({ message, onRetry, size = 'default' }: ErrorStateProps) {
  const { t } = useTranslation('common')
  const text = message ?? t('errors.generic')
  const isSm = size === 'sm'
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center gap-3 rounded-2xl bg-muted text-center shadow-neu-inset',
        isSm ? 'px-4 py-10' : 'px-6 py-16',
      )}
    >
      <AlertTriangle className={cn('text-destructive', isSm ? 'h-8 w-8' : 'h-10 w-10')} aria-hidden />
      <p className="text-sm text-muted-foreground">{text}</p>
      {onRetry && (
        <Button variant="outline" size={isSm ? 'sm' : 'default'} onClick={onRetry}>{t('actions.retry')}</Button>
      )}
    </div>
  )
}
