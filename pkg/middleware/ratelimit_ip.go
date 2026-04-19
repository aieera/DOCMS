package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type ipLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     int
	burst    int
	window   time.Duration
}

type visitor struct {
	lastSeen time.Time
	tokens   int
}

// NewIPRateLimiter returns a middleware that rate-limits by IP.
func NewIPRateLimiter(rate, burst int, window time.Duration) func(http.Handler) http.Handler {
	l := &ipLimiter{
		visitors: make(map[string]*visitor),
		rate:     rate,
		burst:    burst,
		window:   window,
	}
	go l.cleanup()
	return l.limit
}

func (l *ipLimiter) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr // fallback
		}
		l.mu.Lock()
		v, ok := l.visitors[ip]
		now := time.Now()
		if !ok || now.Sub(v.lastSeen) > l.window {
			v = &visitor{lastSeen: now, tokens: l.burst - 1}
			l.visitors[ip] = v
		} else {
			if v.tokens <= 0 {
				l.mu.Unlock()
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
			v.tokens--
			v.lastSeen = now
		}
		l.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (l *ipLimiter) cleanup() {
	for {
		time.Sleep(l.window)
		l.mu.Lock()
		now := time.Now()
		for ip, v := range l.visitors {
			if now.Sub(v.lastSeen) > l.window {
				delete(l.visitors, ip)
			}
		}
		l.mu.Unlock()
	}
}
