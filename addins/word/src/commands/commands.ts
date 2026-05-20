// Office commands shim. The manifest's FunctionFile points at
// commands.html which loads this script. We don't currently
// register UI-less ribbon actions; this file is the future home
// of a "Quick save to last workspace" command that calls Word.run
// + saveCurrentDocument without opening the task pane.
declare const Office: {
  onReady: (cb: () => void) => void
  actions?: {
    associate: (name: string, fn: (arg: unknown) => void) => void
  }
}

Office.onReady(() => {
  // Phase 2 — `quickSaveToLastWorkspace` registers here.
})
