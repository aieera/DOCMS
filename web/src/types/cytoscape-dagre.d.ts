// cytoscape-dagre ships no types. The plugin's only public surface is
// a callable extension registered via `cytoscape.use(dagre)`, so a
// minimal ambient declaration is enough — we never construct or
// inspect the value beyond passing it to .use().
declare module 'cytoscape-dagre' {
  import type cytoscape from 'cytoscape'
  const ext: cytoscape.Ext
  export default ext
}
