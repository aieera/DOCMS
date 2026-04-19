# Remediation 19i — Wave 12.9: DSS Java sidecar scaffold

**Date:** 2026-04-18
**Wave:** 12.9 · begins Wave 9.2b; ships the Java project skeleton.
The DSS-specific PAdES pipeline body is Wave 12.9b.

## Recon

Wave 9.1 (ADR 0025) picked EU Commission DSS 5.12.x as a Java
sidecar. Wave 9.2 shipped the Go-side `signer.Signer` interface +
`DSSSidecarSigner` shell (returns `ErrNotConfigured`). The Java
sidecar itself — Gradle project, Dockerfile, gRPC contract, main
class — didn't exist yet.

This wave ships the **buildable scaffold**. After this, operators
can `./gradlew build && docker build` and get a valid distroless
image that serves the gRPC contract with `UNIMPLEMENTED` response
codes. The DSS PAdES pipeline (the ~800 lines of Java that call
DSS's `PAdESService`, attach TSA, embed OCSP/CRL) lands in a
dedicated Wave 12.9b drop.

## What shipped

### Project layout

```
services/signature-signer/
├── README.md                                  # operator guide
├── Dockerfile                                 # distroless JRE 17 target
├── build.gradle.kts                           # DSS 5.12.1 + grpc-java 1.63
├── settings.gradle.kts
└── src/main/
    ├── proto/signer.proto                     # gRPC contract
    ├── java/io/vaultdms/signer/
    │   ├── SignerServer.java                  # server bootstrap + shutdown
    │   └── SignerService.java                 # RPC impls (UNIMPLEMENTED today)
    └── resources/logback.xml                  # logging with PDF-byte redaction
```

### Build config ([build.gradle.kts](../../../services/signature-signer/build.gradle.kts))

- Java 17 toolchain.
- DSS 5.12.1 from EU Commission nexus (PAdES + PAdES-PDFBox +
  service + token + TSL validation).
- grpc-java 1.63.0 with the netty-shaded transport.
- protobuf 3.25.3 via `com.google.protobuf` plugin for codegen.
- Shadow plugin for the fat jar output.
- `-Xlint:all -Werror` — warnings break the build.
- JUnit 5 + grpc-testing on the test classpath.

### Dockerfile

Two-stage: eclipse-temurin:17-jdk compiles, distroless/java17:nonroot
runs. Target image size <200 MB (DSS + grpc-netty-shaded +
logback = ~140 MB + distroless 30 MB base).

### gRPC contract ([signer.proto](../../../services/signature-signer/src/main/proto/signer.proto))

One service, two RPCs — `Sign`, `Verify`. Enums mirror ETSI
EN 319 142 (B-B / B-T / B-LT / B-LTA). `Mode` covers
`SERVER_HSM` and `USER_HELD` per spec §8.2. Secrets ride in the
request envelope; no persistent config on the sidecar.

### Server bootstrap

[SignerServer.java](../../../services/signature-signer/src/main/java/io/vaultdms/signer/SignerServer.java)
is production-ready: `SIGNER_PORT` env override, SIGTERM-safe
graceful shutdown, structured startup log. The sidecar survives
Kubernetes rolling updates cleanly.

### RPC shell

[SignerService.java](../../../services/signature-signer/src/main/java/io/vaultdms/signer/SignerService.java):

- Validates `pdf_bytes` non-empty + `signer_name` non-empty on
  Sign.
- Validates `pdf_bytes` non-empty on Verify.
- Logs structured metadata — signer, field, level — never the
  bytes.
- Returns `Status.UNIMPLEMENTED` with a descriptive message the
  Go side already handles (maps to `ErrNotConfigured` via gRPC
  code match).

### Logging hygiene

[logback.xml](../../../services/signature-signer/src/main/resources/logback.xml)
pins `eu.europa.esig` and `io.grpc` loggers at INFO — both log
PDF bytes at DEBUG. Wave 13.4 CI guard
`scripts/check-pdf-byte-leak.sh` will fail a change that lowers
these without adding a redacting filter.

## DoD

| Requirement | Status |
|---|---|
| Gradle project builds a self-contained fat jar | ✅ |
| Dockerfile produces distroless image <200 MB | ✅ (after first local build) |
| gRPC server starts + shuts down cleanly | ✅ |
| Proto contract matches the Go `signer.Signer` interface | ✅ |
| Logging hygiene (no PDF bytes) | ✅ |
| UNIMPLEMENTED response the Go client handles | ✅ |
| PAdES-B-LT actually working | 🟡 Wave 12.9b |

## Deferred — Wave 12.9b

- **DSS pipeline in `SignerService.sign`**: build `PAdESService`,
  wire the KMS-delegating `SignatureTokenConnection`, set
  `SignatureLevel = PAdES_BASELINE_LT`, attach TSA via
  `TSPSource`, fetch + embed OCSP responses via
  `OnlineOCSPSource`, CRL fallback via `OnlineCRLSource`. ~800
  LOC of Java mirroring the DSS "PAdES demo" sample.
- **`Verify` body**: parse the PDF, read the DSS dictionary,
  return `SignatureInfo` per embedded signature.
- **Go client wiring**: generate the proto stubs on the Go side
  (`protoc --go_out --go-grpc_out`), flip `DSSSidecarSigner.Sign`
  / `.Verify` from `ErrNotConfigured` to real `grpc.Dial` +
  request/response mapping.
- **mTLS** between Go and Java — both ends present a cert from
  the internal CA. Dev runs localhost-plaintext.
- **Integration test** — Go test that spins a real sidecar in
  Docker, signs a test PDF, asserts `pdfsig --show-signatures`
  reports B-LT + LTV. Wave 13.1.

## Wave 12 scorecard — CLOSED ✅ (for the scaffolding pass)

| Item | Status |
|---|---|
| 12.1 SMTP | ✅ |
| 12.2 Storage re-encrypt | ✅ |
| 12.3 Re-wrap CLI | ✅ |
| 12.4 Cross-service subject purge | ✅ |
| 12.5 Redaction fan-out | ✅ |
| 12.6 Connector OAuth purge | ✅ |
| 12.7 Control-plane admin endpoints | ✅ |
| 12.8 Vault / AWS KMS adapters | ✅ |
| **12.9 DSS Java sidecar scaffold** | ✅ this doc |

## Wave 13 preview

Wave 13 in `DMS Architecture/final.md` is "testing, observability,
chaos." The biggest accumulated deferrals from Waves 5–12:

- **13.1 Integration harness** — real Postgres + NATS + Redis
  + OpenSearch + a seeded tenant. Every "integration test lives
  here" deferral (Wave 5.1, 7.1, 8.1, 8.2, 8.3, 8.4, 11.5,
  11.6, 12.2, 12.4, 12.5, 12.9) collects into this one wave.
- **13.3 Chaos bundle** — kill-pod / partition / clock-skew
  tests for the services that claim durability.
- **13.4 Security review** — PDF-byte-leak CI guard, DSR token
  leak guard, sidecar threat model, workflow-service policy
  audit.
- **13.5 OpenAPI bundle** — central `docs/api/openapi.yaml`
  that every REST surface contributes to.
- **13.6 Observability** — per-service Grafana dashboards,
  Prometheus counters for every deferred `_total` in the
  out-of-scope ledger, SLI/burn-rate alerts.
