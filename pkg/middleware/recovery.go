package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/logger"
	"google.golang.org/grpc"
)

// RecoveryHTTP catches panics in downstream handlers, logs the stack with
// the correlation ID, and returns a sanitized 500 to the client.
func RecoveryHTTP(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error(r.Context()).
						Interface("panic", rec).
						Bytes("stack", debug.Stack()).
						Msg("panic recovered")
					writeInternal(w, r, "unexpected error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RecoveryInterceptor is the gRPC equivalent.
func RecoveryInterceptor(log *logger.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (_ any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error(ctx).
					Interface("panic", rec).
					Bytes("stack", debug.Stack()).
					Msg("panic recovered")
				err = vdmserr.ToGRPCError(vdmserr.ErrInternal)
			}
		}()
		return handler(ctx, req)
	}
}

// ---- shared helpers for HTTP middleware ------------------------------------

func writeUnauthorized(w http.ResponseWriter, r *http.Request, msg string) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"type":           "UNAUTHORIZED",
		"message":        msg,
		"correlation_id": auth.GetCorrelationID(r.Context()),
	})
}

func writeInternal(w http.ResponseWriter, r *http.Request, _ string) {
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"type":           "INTERNAL",
		"message":        "internal error",
		"correlation_id": auth.GetCorrelationID(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// We've already committed the status; just log to stderr.
		fmt.Fprintf(w, `{"type":"INTERNAL","message":"encode failed"}`)
	}
}
