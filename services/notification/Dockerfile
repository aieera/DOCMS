FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /build
COPY go.work ./
COPY pkg/ ./pkg/
COPY services/notification/ ./services/notification/

ARG VERSION=dev
RUN cd services/notification && \
    CGO_ENABLED=0 go build -ldflags="-w -s -X main.version=${VERSION}" -o /app ./cmd/server

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 1000 appuser
USER appuser
COPY --from=builder /app /app
EXPOSE 8080 9090 8081
ENTRYPOINT ["/app"]
