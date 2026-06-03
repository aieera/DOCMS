import { Spinner } from '@/components/ui/Spinner'

export function LoadingScreen() {
  return (
    <div className="flex h-screen items-center justify-center">
      <div className="flex flex-col items-center gap-3">
        <span className="text-2xl font-bold text-[var(--color-primary)]">SeDoc</span>
        <Spinner className="h-6 w-6" />
      </div>
    </div>
  )
}
