package api

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// responseWriter wraps a standard http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("http.Hijacker not implemented")
}

func (rw *responseWriter) Flush() {
	if fl, ok := rw.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// LoggerMiddleware logs every HTTP request using slog.
func LoggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{w, http.StatusOK}

		next.ServeHTTP(rw, r)

		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration", time.Since(start),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// RecoveryMiddleware catches panics and logs them.
func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic recovered",
					"error", err,
					"stack", string(debug.Stack()),
					"path", r.URL.Path,
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// SecurityHeadersMiddleware adds standard web security headers.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if os.Getenv("HTTPS_ENABLED") == "true" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}

// CORSMiddleware handles CORS with configurable allowed origins.
// Set CORS_ORIGINS env to comma-separated origins, or leave empty for "*".
func CORSMiddleware(next http.Handler) http.Handler {
	allowedOrigins := os.Getenv("CORS_ORIGINS")
	originSet := map[string]bool{}
	if allowedOrigins != "" {
		for _, o := range strings.Split(allowedOrigins, ",") {
			originSet[strings.TrimSpace(o)] = true
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if len(originSet) == 0 {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if originSet[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}

		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BodyLimitMiddleware restricts the maximum request body size.
func BodyLimitMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimiter implements a simple token-bucket rate limiter per client IP.
type RateLimiter struct {
	mu      sync.Mutex
	clients map[string]*rateBucket
	rate    int           // tokens per interval
	burst   int           // max tokens
	window  time.Duration // refill interval
}

type rateBucket struct {
	tokens     int
	lastRefill time.Time
}

// NewRateLimiter creates a rate limiter. rate = requests allowed per window.
// burst = maximum burst capacity (typically same as rate).
func NewRateLimiter(rate, burst int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		clients: make(map[string]*rateBucket),
		rate:    rate,
		burst:   burst,
		window:  window,
	}
	// Background cleanup of stale entries
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			rl.mu.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			for ip, b := range rl.clients {
				if b.lastRefill.Before(cutoff) {
					delete(rl.clients, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

// Allow checks whether the given IP is within the rate limit.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.clients[ip]
	now := time.Now()

	if !ok {
		rl.clients[ip] = &rateBucket{tokens: rl.burst - 1, lastRefill: now}
		return true
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastRefill)
	refill := int(elapsed/rl.window) * rl.rate
	if refill > 0 {
		b.tokens += refill
		if b.tokens > rl.burst {
			b.tokens = rl.burst
		}
		b.lastRefill = now
	}

	if b.tokens > 0 {
		b.tokens--
		return true
	}
	return false
}

// Middleware returns an http middleware that enforces the rate limit.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !rl.Allow(ip) {
			slog.Warn("rate limit exceeded", "ip", ip, "path", r.URL.Path)
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the real client IP, respecting X-Forwarded-For
// from trusted proxies.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
