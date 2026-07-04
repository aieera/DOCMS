// runBatched — process `items` through an async `worker` with a fixed
// concurrency cap (a worker pool), reporting progress as each finishes.
//
// Used by bulk document operations (delete/download/...) so a large
// selection doesn't fire N requests at once (which freezes the tab and
// hammers the server) and so the UI can show live progress + a per-item
// result. Never rejects: a failing item is captured as { ok:false, error }
// and the run continues, so the caller gets a full partial-failure report.
export interface BatchItemResult {
  index: number
  ok: boolean
  error?: string
}

export async function runBatched<T>(
  items: T[],
  worker: (item: T, index: number) => Promise<void>,
  opts: { concurrency?: number; onProgress?: (done: number, total: number) => void } = {},
): Promise<BatchItemResult[]> {
  const concurrency = Math.max(1, opts.concurrency ?? 5)
  const results: BatchItemResult[] = new Array(items.length)
  let cursor = 0
  let done = 0

  const runWorker = async (): Promise<void> => {
    while (cursor < items.length) {
      const i = cursor++
      try {
        await worker(items[i], i)
        results[i] = { index: i, ok: true }
      } catch (e) {
        results[i] = { index: i, ok: false, error: e instanceof Error ? e.message : String(e) }
      }
      done++
      opts.onProgress?.(done, items.length)
    }
  }

  await Promise.all(
    Array.from({ length: Math.min(concurrency, items.length) }, () => runWorker()),
  )
  return results
}
