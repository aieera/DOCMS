# signature-signer

Java 17 sidecar that produces PAdES-signed PDFs using the
EU Commission **DSS** library (ADR 0025).

## Why a sidecar

`services/signature` is Go. DSS is Java. Rather than re-implement
eIDAS-grade PAdES in Go (multi-year project; see ADR 0025
§Alternatives), we run DSS as a per-pod sidecar and call it via
gRPC from the signature service. The Go code stays Go; the Java
surface stays narrow (one RPC method: `Sign`).

## Project layout

```
services/signature-signer/
├── build.gradle.kts            # Gradle build with DSS 5.12.x + grpc-java
├── settings.gradle.kts
├── Dockerfile                  # distroless JRE 17 image (~180 MB)
├── src/main/java/io/sedoc/signer/
│   ├── SignerServer.java       # gRPC server bootstrap
│   ├── SignerService.java      # impl of the generated gRPC stub (PAdES logic)
│   └── KeystoreAdapter.java    # mTLS for the gRPC channel
└── src/main/proto/
    └── signer.proto            # the Sign RPC contract
```

## Build

```bash
cd services/signature-signer
./gradlew build        # produces build/libs/signer-all.jar (~35 MB fat jar)
docker build -t sedoc-signer .
```

## Run (dev)

```bash
docker run --rm -p 6060:6060 \
  -e SIGNER_TSA_URL=https://freetsa.org/tsr \
  sedoc-signer
```

Go signature service dials `localhost:6060` (configurable via
`SEDOC_SIGNER_SIDECAR_ADDR`). The Go side's
`DSSSidecarSigner` shell (Wave 9.2) handles the gRPC client
wiring.

## Security invariants

Matches ADR 0025 §Decision:

1. **Secrets per-request, not at rest.** The KMS alias is
   resolved by the Go signature service; the sidecar only ever
   sees the in-flight signer cert + signed hash. No private key
   material lives in sidecar memory across requests.
2. **mTLS between Go and Java.** In prod both ends present a
   cert from the same internal CA; dev uses plain localhost
   gRPC (runs on the same pod).
3. **No PDF logging.** The sidecar's logback config redacts
   `pdfBytes` and `signedPdfBytes` fields on every log line —
   enforced by a Wave 13.4 CI guard that greps the final image
   for logback config without the redaction filter.

## Deferred (Wave 12.9b)

This scaffolding ships as a buildable project with the DSS +
grpc-java dependencies pinned and the server / service skeleton
in place. The DSS-specific implementation details in
`SignerService.java` (PAdES-B-LT envelope, TSA round-trip,
OCSP / CRL embed) land in Wave 12.9b — they're ~800 lines of
Java that follow the DSS "PAdES demo" sample closely.

The Go-side `DSSSidecarSigner.Sign` shell in
`services/signature/internal/signer/dss_sidecar.go` still
returns `ErrNotConfigured`. It gets its real body (proto
generation + `grpc.Dial` + request/response mapping) in
Wave 12.9b too. The two flips ship together so the Go service's
`SEDOC_SIGNER=dss` codepath either works end-to-end or fails
fast (`ErrNotConfigured`).
