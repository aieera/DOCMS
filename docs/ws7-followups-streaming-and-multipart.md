# WS7 follow-ups — streaming decrypt & multipart upload

The other four WS7 hardening items shipped in the same change as this note
(search `search_after`, ListDocuments per-row ACL, bulk parallelization,
integration-route rate limiting). The two below were deliberately deferred: each
is a cross-service / data-format change that must not be rushed, and the crypto
one cannot be verified end-to-end in CI without the Python worker. This is the
concrete design for picking them up as focused workstreams.

---

## Item 1 — Stream-decrypt blob downloads at flat memory

### Why it's not a one-liner
Blobs are encrypted with **whole-file AES-256-GCM**: a single nonce + a single
auth tag over the entire plaintext (`pkg/crypto/envelope.go` `EncryptData`/
`DecryptData`, called by `services/storage/internal/service/encryption.go`). GCM
`Open` can only verify the tag after the **whole** ciphertext is in memory, so
the current download path (`services/document/internal/handler/decrypt_stream.go`,
`io.ReadAll(obj)` → `DecryptData` → `w.Write`) holds ~2× the file in RAM. No
amount of wrapping makes single-tag GCM stream safely — the format has to change.

### Design: chunked AEAD (framed GCM), backward-compatible
1. **New codec** `pkg/crypto/stream.go`:
   - Encrypt: split plaintext into fixed frames (e.g. 1 MiB). Each frame is
     sealed with AES-256-GCM using `nonce = baseNonce || uint32(frameIndex)` and
     AAD `= uint64(frameIndex) || isFinalFlag` (binds order + prevents
     truncation/reordering). Output = `magic(4) || version(1) || baseNonce(8) ||
     frame[0] || frame[1] || …`.
   - Decrypt: `StreamDecryptReader` implementing `io.Reader` — read one frame,
     `Open`+verify, yield its plaintext, repeat. Flat memory = one frame.
   - The final frame carries a terminator flag in its AAD so a truncated stream
     fails closed instead of returning a short read.
2. **Format marker for back-compat**: detect by the 4-byte magic header. No magic
   → legacy whole-file GCM → current full-buffer path. Magic present → stream. No
   schema column needed; existing blobs keep working untouched.
3. **Storage write side** (`encryption.go` `encryptAll`): emit the chunked format
   for NEW uploads. (Storage still buffers the plaintext for SHA/scan/MIME — that's
   the upload path, out of scope for the *download* acceptance — but the on-disk
   bytes become streamable.)
4. **Document download side** (`decrypt_stream.go` + `share_download_handler.go`):
   `io.Copy(w, StreamDecryptReader(s3Obj, dek))` with chunked transfer encoding →
   flat memory for the multi-hundred-MB case.
5. **Python worker** (`services/intelligence/app/storage/envelope.py`,
   `app/tasks/ocr.py::_decrypt_src_if_envelope`): MUST learn the chunked format,
   since it decrypts blobs for OCR. **This is the risk gate** — the Go and Python
   framing must be byte-identical or OCR breaks / downloads corrupt.

### Test plan (the part that needs its own workstream)
- Go unit: encrypt→decrypt roundtrip across frame boundaries (size = k·frame,
  k·frame±1, < frame, 0); tamper a byte in frame N → `Open` fails; truncate the
  final frame → fails closed.
- **Cross-language golden vectors**: a fixed (key, nonce, plaintext) → committed
  ciphertext bytes, asserted by BOTH a Go test and a Python test, so the two
  implementations can't silently diverge.
- Memory: download a ~300 MB blob, assert RSS stays flat (frame-sized), not O(file).

### Effort
~2–3 days incl. cross-language vectors + a backfill decision (existing blobs stay
legacy-format forever, which is fine — they decrypt via the legacy path).

---

## Item 5 — Multipart upload (the enum already advertises it)

### Current state
`model.UploadType` already declares `single|multipart|tus` and `UploadSession`
already has `S3UploadID`, `PartsCompleted`, `PartsTotal`
(`services/storage/internal/service/model.go`), but `InitiateUpload` hardcodes
`UploadSingle` and only single-PUT (≤5 GiB) is wired. The S3 wrapper
(`pkg/storage/s3.go`) exposes only `PutObject`/`GetObject`/presigned single PUT —
no multipart ops.

### Design
1. **S3 wrapper** `pkg/storage/s3.go` — add, delegating to minio-go:
   `CreateMultipartUpload`, `PresignUploadPart(uploadID, partNumber)`,
   `CompleteMultipartUpload(parts []CompletedPart)`, `AbortMultipartUpload`.
2. **Proto** `proto/sedoc/v1/storage.proto` — additive RPCs:
   - `InitiateUpload` gains an `upload_type` + `part_size`; for multipart it
     returns the `s3_upload_id` instead of a single presigned PUT URL.
   - `GetUploadPartURL(upload_id, part_number) → presigned_url` (called per part).
   - `CompleteUpload` gains a `repeated Part parts {number, etag}` for the
     multipart finalize.
   - `AbortUpload` already exists — wire it to `AbortMultipartUpload`.
3. **Storage service** (`service.go`): branch `InitiateUpload` on `upload_type`;
   persist `S3UploadID` + `PartsTotal`; implement `GetUploadPartURL`; in
   `CompleteUpload`, call `CompleteMultipartUpload` with the client's part etags.
4. **`CompleteUpload` memory** ⚠️: it currently `GetObject`s the whole assembled
   object into RAM for SHA-256 + ClamAV + MIME + encrypt. A multi-GB multipart
   object would blow memory there. The multipart workstream must stream those
   passes (streaming SHA tee, ClamAV `INSTREAM` chunking, MIME from a head read),
   or scan per-part. **This is the real work in item 5**, not the S3 plumbing.
5. **Document proxy** (`storage_proxy.go`): add
   `POST /uploads/{id}/parts/{n}/url` and pass `parts[]` through on complete.

### Effort
~3–4 days, dominated by the streaming-scan rework in `CompleteUpload` (#4).

---

## Sequencing recommendation
Do **item 5's streaming-scan rework** and **item 1's chunked codec** together:
both need streaming I/O over blob bytes and both touch the same
upload/download/scan surface, so the io.Reader plumbing is shared. The Python
golden-vector tests gate item 1's merge.
