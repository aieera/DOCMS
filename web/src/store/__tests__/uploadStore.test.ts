import { describe, it, expect, beforeEach } from 'vitest'
import { useUploadStore } from '@/store/uploadStore'

// M-29: two concurrent upload batches each await a duplicate decision.
// A single-slot prompt let the second request overwrite the first, so
// the first batch's promise never resolved and its files stranded in
// `pending`. Prompts are now a FIFO queue.
describe('uploadStore duplicate prompts (M-29)', () => {
  beforeEach(() => useUploadStore.getState().reset())

  it('two batches both resolve', async () => {
    const s = useUploadStore.getState()
    const first = s.requestDuplicateDecision('a.pdf', [])
    const second = s.requestDuplicateDecision('b.pdf', [])

    // Head of the queue is shown first.
    expect(useUploadStore.getState().duplicatePrompt?.fileName).toBe('a.pdf')
    useUploadStore.getState().resolveDuplicate(true)
    await expect(first).resolves.toBe(true)

    // Then the second batch's prompt.
    expect(useUploadStore.getState().duplicatePrompt?.fileName).toBe('b.pdf')
    useUploadStore.getState().resolveDuplicate(false)
    await expect(second).resolves.toBe(false)

    expect(useUploadStore.getState().duplicatePrompt).toBeNull()
  })

  it('reset releases every queued prompt as skip', async () => {
    const s = useUploadStore.getState()
    const p1 = s.requestDuplicateDecision('a.pdf', [])
    const p2 = s.requestDuplicateDecision('b.pdf', [])
    useUploadStore.getState().reset()
    await expect(p1).resolves.toBe(false)
    await expect(p2).resolves.toBe(false)
    expect(useUploadStore.getState().duplicatePrompt).toBeNull()
  })
})
