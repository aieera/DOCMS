// Bootstrap entry — wires the TaskPane into the host page once
// Office signals it's ready. We deliberately don't render anything
// before Office.onReady fires: Office.context.mailbox is undefined
// until then, and any component that touches it throws.
import { createRoot } from 'react-dom/client'
import { FluentProvider, webLightTheme, webDarkTheme } from '@fluentui/react-components'

import { TaskPane } from './TaskPane'

declare const Office: {
  onReady: (cb: (info: { host: string; platform: string }) => void) => void
  context: { mailbox: { item: unknown } }
}

Office.onReady(() => {
  const root = createRoot(document.getElementById('root')!)
  // Honour the host's color theme. Outlook on Mac defaults to dark,
  // Windows users frequently switch theme mid-session; reading from
  // Office.context.officeTheme would be authoritative but it's the
  // newer surface. Body class is the cheap fallback.
  const prefersDark = document.body.classList.contains('ms-Fabric--theme-dark')
    || matchMedia('(prefers-color-scheme: dark)').matches
  root.render(
    <FluentProvider theme={prefersDark ? webDarkTheme : webLightTheme}>
      <TaskPane />
    </FluentProvider>,
  )
})
