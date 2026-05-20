// Office commands shim. The manifest's FunctionFile points at
// commands.html which loads this script. We don't currently register
// any UI-less ribbon actions, but Office still expects
// Office.actions.associate() handlers to live here when they're
// added. Empty file = compliant + future-proofed.
declare const Office: {
  onReady: (cb: () => void) => void
  actions?: {
    associate: (name: string, fn: (arg: unknown) => void) => void
  }
}

Office.onReady(() => {
  // Phase 2 — `saveToDefaultWorkspace` registers here so an admin
  // can configure a one-click ribbon action that bypasses the
  // taskpane entirely.
})
