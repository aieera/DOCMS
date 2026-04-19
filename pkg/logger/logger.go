// Package logger provides a zerolog-based structured logger used by every
// VaultDMS service. Every log line includes service name + version, and —
// when the provided context carries them — tenant_id and correlation_id.
//
// PII scrubbing helpers are exposed for use in application code when a value
// must be logged but cannot be logged verbatim.
package logger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// Context keys. Duplicated in pkg/auth to avoid an import cycle; the two keys
// must use identical underlying values so either package can produce/consume
// them. Using a named type guarantees a unique context key.
type ctxKey string

const (
	// TenantIDKey is the context key under which the tenant UUID string is stored.
	TenantIDKey ctxKey = "tenant_id"
	// CorrelationIDKey is the context key under which the correlation ID is stored.
	CorrelationIDKey ctxKey = "correlation_id"
	// UserIDKey is the context key under which the authenticated user UUID string is stored.
	UserIDKey ctxKey = "user_id"
)

// Logger wraps zerolog with context-aware helpers.
type Logger struct {
	zl              zerolog.Logger
	debugSampleRate float64 // 0 = log all debug, 0.01 = 1% in prod
}

// New builds a Logger writing JSON to stdout. level is "debug", "info",
// "warn", or "error". Unknown levels fall back to info.
func New(serviceName, serviceVersion, level string) *Logger {
	return NewWithWriter(os.Stdout, serviceName, serviceVersion, level)
}

// NewWithWriter is like New but writes to the provided io.Writer. Primarily
// used in tests.
func NewWithWriter(w io.Writer, serviceName, serviceVersion, level string) *Logger {
	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.CallerSkipFrameCount = 3

	zl := zerolog.New(w).
		Level(parseLevel(level)).
		With().
		Timestamp().
		Str("service", serviceName).
		Str("version", serviceVersion).
		Caller().
		Logger()

	l := &Logger{zl: zl}
	if os.Getenv("VAULTDMS_ENVIRONMENT") == "prod" {
		l.debugSampleRate = 0.01 // 1% sampling in production
	}
	return l
}

func parseLevel(s string) zerolog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return zerolog.DebugLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}

// withCtx decorates an event with tenant_id and correlation_id pulled from ctx.
func (l *Logger) withCtx(ctx context.Context, e *zerolog.Event) *zerolog.Event {
	if ctx == nil {
		return e
	}
	if v, ok := ctx.Value(TenantIDKey).(string); ok && v != "" {
		e = e.Str("tenant_id", v)
	}
	if v, ok := ctx.Value(CorrelationIDKey).(string); ok && v != "" {
		e = e.Str("correlation_id", v)
	}
	if v, ok := ctx.Value(UserIDKey).(string); ok && v != "" {
		e = e.Str("user_id", v)
	}
	return e
}

// Info logs at info level, decorating with context fields.
func (l *Logger) Info(ctx context.Context) *zerolog.Event {
	return l.withCtx(ctx, l.zl.Info())
}

// Warn logs at warn level.
func (l *Logger) Warn(ctx context.Context) *zerolog.Event {
	return l.withCtx(ctx, l.zl.Warn())
}

// Error logs at error level.
func (l *Logger) Error(ctx context.Context) *zerolog.Event {
	return l.withCtx(ctx, l.zl.Error())
}

// Debug logs at debug level. In prod (VAULTDMS_ENVIRONMENT=prod), debug
// lines are sampled at 1% to avoid flooding log aggregators.
func (l *Logger) Debug(ctx context.Context) *zerolog.Event {
	if l.debugSampleRate > 0 && rand.Float64() > l.debugSampleRate {
		nop := zerolog.Nop()
		return nop.Debug()
	}
	return l.withCtx(ctx, l.zl.Debug())
}

// Fatal logs at fatal level then calls os.Exit(1).
func (l *Logger) Fatal(ctx context.Context) *zerolog.Event {
	return l.withCtx(ctx, l.zl.Fatal())
}

// Z returns the underlying zerolog.Logger for niche cases (e.g. passing to a
// third-party library that wants a *zerolog.Logger).
func (l *Logger) Z() *zerolog.Logger { return &l.zl }

// ---- PII scrubbing ---------------------------------------------------------

var emailRegex = regexp.MustCompile(`(?i)^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)

// HashEmail returns a stable, non-reversible hash of an email address safe
// for logs and analytics. Returns empty string for empty input.
func HashEmail(email string) string {
	if email == "" {
		return ""
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if !emailRegex.MatchString(email) {
		return "invalid"
	}
	sum := sha256.Sum256([]byte(email))
	return "sha256:" + hex.EncodeToString(sum[:12])
}

// HashIP returns a hashed representation of an IP address (v4 or v6). For v4,
// the last octet is zeroed before hashing to preserve approximate locality
// for analytics while reducing re-identification risk.
func HashIP(ip string) string {
	if ip == "" {
		return ""
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return "invalid"
	}
	if v4 := parsed.To4(); v4 != nil {
		v4[3] = 0
		parsed = v4
	}
	sum := sha256.Sum256([]byte(parsed.String()))
	return "sha256:" + hex.EncodeToString(sum[:12])
}
