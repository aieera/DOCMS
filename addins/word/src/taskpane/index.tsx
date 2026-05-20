// Bootstrap entry. We deliberately don't render anything before
// Office.onReady fires — Word.run() + Office.context.document are
// undefined until then and any component that touches them throws.
import { createRoot } from 'react-dom/client'
import { FluentProvider, webLightTheme, webDarkTheme } from '@fluentui/react-components'

import { TaskPane } from './TaskPane'

declare const Office: {
  onReady: (cb: (info: { host: string; platform: string }) => void) => void
}

Office.onReady(() => {
  const root = createRoot(document.getElementById('root')!)
  const prefersDark = matchMedia('(prefers-color-scheme: dark)').matches
  root.render(
    <FluentProvider theme={prefersDark ? webDarkTheme : webLightTheme}>
      <TaskPane />
    </FluentProvider>,
  )
})
