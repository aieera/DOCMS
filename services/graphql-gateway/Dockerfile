# Build from repo root:  docker build -f services/graphql-gateway/Dockerfile .
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /build
COPY go.work ./
COPY pkg/ ./pkg/
COPY proto/ ./proto/
COPY services/graphql-gateway/ ./services/graphql-gateway/

ARG VERSION=dev
RUN cd services/graphql-gateway && \
    CGO_ENABLED=0 go build -ldflags="-w -s -X main.version=${VERSION}" -o /app ./cmd/server

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 appuser
USER appuser
COPY --from=builder /app /app
EXPOSE 8080 8081
ENTRYPOINT ["/app"]
