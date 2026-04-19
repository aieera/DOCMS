package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/redis/go-redis/v9"
)

// CORSConfig per-tenant CORS settings cached in Redis.
type CORSConfig struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	MaxAge         int
}

// DefaultCORS for tenants without custom config.
var DefaultCORS = CORSConfig{
	AllowedOrigins: []string{"*"},
	AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
	AllowedHeaders: []string{"Authorization", "Content-Type", "X-Tenant-ID", "X-User-ID", "X-Correlation-ID"},
	MaxAge:         3600,
}

// CORSMiddleware applies per-tenant CORS. Reads tenant-specific origins
// from Redis (key cors:{tenant_id}); falls back to DefaultCORS.
func CORSMiddleware(rdb *redis.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			tenantID := r.Header.Get("X-Tenant-ID")

			cfg := DefaultCORS
			if tenantID != "" && rdb != nil {
				if raw, err := rdb.Get(context.Background(), "cors:"+tenantID).Result(); err == nil && raw != "" {
					cfg.AllowedOrigins = strings.Split(raw, ",")
				}
			}

			allowed := false
			for _, o := range cfg.AllowedOrigins {
				if o == "*" || o == origin {
					allowed = true
					break
				}
			}

			if allowed && origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowedMethods, ", "))
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
			w.Header().Set("Access-Control-Max-Age", "3600")
			w.Header().Set("Access-Control-Allow-Credentials", "true")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
