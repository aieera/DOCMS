# Go build for the integration binaries (worker | bff | mockerp).
# Build context is the REPO ROOT so we can pull in the shared pkg module; we
# synthesize a minimal go.work containing only the two modules the integration
# needs (pkg + integration), avoiding the full 16-module workspace.
#
#   docker build -f integration/deploy/Dockerfile --build-arg BIN=worker -t int-worker .
FROM golang:1.26-bookworm AS build
ARG BIN=worker
WORKDIR /src
COPY pkg/ ./pkg/
COPY integration/ ./integration/
# No go.work in the image — instead resolve the shared module via a local
# replace, so `require github.com/aieera/sedoc/pkg v0.0.0` uses ../pkg rather
# than attempting a git fetch of a private repo.
ENV GOWORK=off
WORKDIR /src/integration
RUN go mod edit -replace github.com/aieera/sedoc/pkg=../pkg \
 && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -trimpath -o /out/app ./cmd/${BIN}

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build /out/app /app/app
# The worker runs migrations from the relative "migrations" dir (CWD = /app).
COPY integration/migrations/ /app/migrations/
ENTRYPOINT ["/app/app"]
