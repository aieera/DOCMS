// sha256HexOfFile computes the lowercase-hex SHA-256 of a File for the
// pre-upload duplicate check and to activate storage-side early dedup.
//
// Returns null (rather than throwing) when hashing isn't viable, so
// callers degrade gracefully to the no-hash upload path:
//   - file is empty (0 bytes have no meaningful content hash)
//   - file exceeds maxBytes — hashing reads the whole file into memory
//     via arrayBuffer(), so we skip very large files to avoid OOM
//   - crypto.subtle is unavailable (insecure http context that isn't
//     localhost) or File.arrayBuffer is missing (older/test envs)
export async function sha256HexOfFile(
  file: File,
  maxBytes = 256 * 1024 * 1024,
): Promise<string | null> {
  try {
    if (file.size === 0 || file.size > maxBytes) return null
    if (!globalThis.crypto?.subtle || typeof file.arrayBuffer !== 'function') return null
    const buf = await file.arrayBuffer()
    const digest = await globalThis.crypto.subtle.digest('SHA-256', buf)
    return Array.from(new Uint8Array(digest))
      .map((b) => b.toString(16).padStart(2, '0'))
      .join('')
  } catch {
    return null
  }
}
