import { Component, type ErrorInfo, type ReactNode } from 'react'
import { AlertTriangle, RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/shadcn/button'
import { cn } from '@/lib/cn'

interface Props {
  children: ReactNode
  fallback?: ReactNode
  /** Named region, e.g. "Tags" — shown in the fallback so the user
   *  knows WHICH part failed rather than assuming the whole app did. */
  label?: string
  /** `panel` keeps the failure inside its own card (a rail widget or a
   *  tab body); `page` is the full-height treatment for a route root. */
  variant?: 'page' | 'panel'
  onReset?: () => void
}
interface State { hasError: boolean; error?: Error }

// Catches render errors. Two rules learned the hard way:
//
//  1. Scope. A single boundary at the app root meant one broken rail
//     widget ("taskOpen is not defined") blanked the entire shell —
//     nav, header and all. Wrap each panel so a failure stays inside
//     the box that failed.
//  2. Never show users the raw JS message. "taskOpen is not defined"
//     is for the console; the UI gets plain language, and the detail
//     stays available under a disclosure for bug reports.
export class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false }
  static getDerivedStateFromError(error: Error) { return { hasError: true, error } }
  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(`ErrorBoundary${this.props.label ? ` [${this.props.label}]` : ''}:`, error, info)
  }

  private reset = () => {
    this.setState({ hasError: false, error: undefined })
    this.props.onReset?.()
  }

  render() {
    if (!this.state.hasError) return this.props.children
    if (this.props.fallback) return this.props.fallback

    const panel = this.props.variant === 'panel'
    const what = this.props.label ? `“${this.props.label}”` : 'This section'
    return (
      <div
        role="alert"
        className={cn(
          'flex flex-col items-center justify-center gap-3 rounded-lg border border-border bg-card text-center',
          panel ? 'p-6' : 'p-16',
        )}
        data-testid="error-boundary-fallback"
      >
        <span className="grid h-10 w-10 place-items-center rounded-full bg-destructive/10 text-destructive">
          <AlertTriangle className="h-5 w-5" />
        </span>
        <div>
          <p className={cn('font-semibold', panel ? 'text-sm' : 'text-lg')}>
            {what} couldn’t be displayed
          </p>
          <p className="mt-1 max-w-sm text-xs text-muted-foreground">
            The rest of the page is still usable. Retrying often clears it — if it
            keeps happening, send us the details below.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" onClick={this.reset}>
            <RotateCcw className="me-1 h-4 w-4" /> Retry
          </Button>
        </div>
        {this.state.error?.message && (
          <details className="w-full max-w-md text-start">
            <summary className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
              Technical details
            </summary>
            <pre className="mt-1 overflow-x-auto rounded bg-muted p-2 text-[11px] leading-relaxed text-muted-foreground">
              {this.state.error.message}
            </pre>
          </details>
        )}
      </div>
    )
  }
}
