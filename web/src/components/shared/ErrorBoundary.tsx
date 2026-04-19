import { Component, type ErrorInfo, type ReactNode } from 'react'
import { Button } from '@/components/ui/Button'

interface Props { children: ReactNode; fallback?: ReactNode }
interface State { hasError: boolean; error?: Error }

export class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false }
  static getDerivedStateFromError(error: Error) { return { hasError: true, error } }
  componentDidCatch(error: Error, info: ErrorInfo) { console.error('ErrorBoundary:', error, info) }
  render() {
    if (this.state.hasError) {
      return this.props.fallback || (
        <div className="flex flex-col items-center justify-center gap-4 p-16">
          <h2 className="text-lg font-semibold">Something went wrong</h2>
          <p className="text-sm text-[var(--color-text-secondary)]">{this.state.error?.message}</p>
          <Button onClick={() => this.setState({ hasError: false })}>Retry</Button>
        </div>
      )
    }
    return this.props.children
  }
}
