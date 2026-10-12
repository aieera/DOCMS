package middleware

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/aieera/sedoc/pkg/logger"
	"google.golang.org/grpc"
)

// RequestLogHTTP logs one line per HTTP request. /healthz, /readyz, /metrics
// are suppressed to reduce noise.
func RequestLogHTTP(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isHealthPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			rw := &recordingWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(rw, r)

			ua := r.UserAgent()
			if len(ua) > 200 {
				ua = ua[:200]
			}
			log.Info(r.Context()).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", rw.status).
				Int64("latency_ms", time.Since(start).Milliseconds()).
				Int64("request_bytes", r.ContentLength).
				Int("response_bytes", rw.bytes).
				Str("user_agent", ua).
				Str("remote_ip", logger.HashIP(ClientIP(r))).
				Msg("http request")
		})
	}
}

// RequestLogInterceptor is the gRPC equivalent.
func RequestLogInterceptor(log *logger.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		evt := log.Info(ctx).
			Str("method", info.FullMethod).
			Int64("latency_ms", time.Since(start).Milliseconds())
		if err != nil {
			evt = evt.Err(err)
			// vdmserr.ToGRPCError scrubs internal errors to a generic
			// status (M-17) but keeps the real cause behind Unwrap —
			// log it here so operators still see what failed.
			if cause := errors.Unwrap(err); cause != nil {
				evt = evt.AnErr("cause", cause)
			}
		}
		evt.Msg("grpc call")
		return resp, err
	}
}

// ---- internals -------------------------------------------------------------

type recordingWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *recordingWriter) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recordingWriter) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func isHealthPath(p string) bool {
	return p == "/healthz" || p == "/readyz" || p == "/metrics"
}
