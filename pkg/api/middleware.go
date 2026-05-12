package api

import (
	"bufio"
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"

	"github.com/neoromantics/chess/pkg/auth"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chess_http_requests_total",
		Help: "Total number of HTTP requests.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "chess_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path", "status"})

	WebSocketConnectionsActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "chess_websocket_connections_active",
		Help: "Current number of active WebSocket connections.",
	}, []string{"kind"})
)

// MetricsMiddleware records HTTP metrics for prometheus.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{w, http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(rw.status)

		// Aggregate paths to avoid high-cardinality labels (e.g. /api/invites/123 -> /api/invites/:id)
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/invites/") && strings.HasSuffix(path, "/accept") { path = "/api/invites/:id/accept" }
		if strings.HasPrefix(path, "/api/invites/") && strings.HasSuffix(path, "/decline") { path = "/api/invites/:id/decline" }
		if strings.HasPrefix(path, "/api/invites/") && strings.HasSuffix(path, "/cancel") { path = "/api/invites/:id/cancel" }

		httpRequestsTotal.WithLabelValues(r.Method, path, status).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path, status).Observe(duration)
	})
}

// requireAuth wraps a handler and returns 401 when no authenticated user
// is in the request context. Use for endpoints that must not be reachable
// by anonymous sessions — game creation, profile mutation, etc.
func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.GetUser(r.Context()); !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

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

// recordingResponseWriter captures both status and body for idempotency caching.
type recordingResponseWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (rw *recordingResponseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recordingResponseWriter) Write(b []byte) (int, error) {
	rw.body.Write(b)
	return rw.ResponseWriter.Write(b)
}

func (rw *recordingResponseWriter) Flush() {
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
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, Idempotency-Key")
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

// RateLimiter applies a per-IP token-bucket rate limit backed by
// golang.org/x/time/rate. We keep one Limiter per client IP in a map
// and evict stale entries periodically.
type RateLimiter struct {
	mu      sync.Mutex
	clients map[string]*rateClient
	limit   rate.Limit
	burst   int
}

type rateClient struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter creates a rate limiter allowing `rate` requests per
// `window` with a burst capacity of `burst`.
func NewRateLimiter(reqs, burst int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		clients: make(map[string]*rateClient),
		limit:   rate.Limit(float64(reqs) / window.Seconds()),
		burst:   burst,
	}
	go rl.gc()
	return rl
}

func (rl *RateLimiter) gc() {
	for {
		time.Sleep(5 * time.Minute)
		rl.mu.Lock()
		cutoff := time.Now().Add(-10 * time.Minute)
		for ip, c := range rl.clients {
			if c.lastSeen.Before(cutoff) {
				delete(rl.clients, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// Allow reports whether the given IP is within the rate limit.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	c, ok := rl.clients[ip]
	if !ok {
		c = &rateClient{limiter: rate.NewLimiter(rl.limit, rl.burst)}
		rl.clients[ip] = c
	}
	c.lastSeen = time.Now()
	rl.mu.Unlock()
	return c.limiter.Allow()
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
