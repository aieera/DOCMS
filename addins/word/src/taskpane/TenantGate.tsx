// W-10: resolve the SeDoc session before the task pane renders. When the
// Microsoft account belongs to more than one SeDoc tenant the exchange
// raises TenantChoiceRequired; offer a picker instead of dumping the 409
// JSON into an error bar.
import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { Button, Spinner, MessageBar, MessageBarBody, MessageBarTitle } from '@fluentui/react-components'

import { getSession, chooseTenant, TenantChoiceRequired, type TenantCandidate } from '../auth'

type GateState =
  | { kind: 'loading' }
  | { kind: 'ready' }
  | { kind: 'choose'; candidates: TenantCandidate[] }
  | { kind: 'error'; message: string }

export function TenantGate({ children }: { children: ReactNode }) {
  const [state, setState] = useState<GateState>({ kind: 'loading' })

  const attempt = useCallback(() => {
    setState({ kind: 'loading' })
    getSession()
      .then(() => setState({ kind: 'ready' }))
      .catch((e: unknown) => {
        if (e instanceof TenantChoiceRequired) setState({ kind: 'choose', candidates: e.candidates })
        else setState({ kind: 'error', message: (e as Error)?.message ?? 'Sign-in failed' })
      })
  }, [])

  useEffect(() => { attempt() }, [attempt])

  if (state.kind === 'ready') return <>{children}</>
  if (state.kind === 'loading') return <Spinner label="Signing in to SeDoc…" />
  if (state.kind === 'error') {
    return (
      <div style={{ padding: 12 }}>
        <MessageBar intent="error">
          <MessageBarBody>{state.message}</MessageBarBody>
        </MessageBar>
        <Button style={{ marginTop: 8 }} onClick={attempt}>Try again</Button>
      </div>
    )
  }
  return (
    <div style={{ padding: 12, display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="tenant-picker">
      <MessageBar intent="info">
        <MessageBarBody>
          <MessageBarTitle>Choose your organisation</MessageBarTitle>
          Your account belongs to more than one SeDoc organisation.
        </MessageBarBody>
      </MessageBar>
      {state.candidates.map((c) => (
        <Button
          key={c.tenant_id}
          appearance="secondary"
          onClick={() => { chooseTenant(c.tenant_id); attempt() }}
        >
          {c.tenant_name || c.tenant_slug}
        </Button>
      ))}
    </div>
  )
}
