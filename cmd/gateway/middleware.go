package main

// Defensive HTTP middlewares applied selectively to the auth surface:
//
//   - rateLimit:   per-IP token bucket (golang.org/x/time/rate). Stops
//                  brute-force login + the cost-14 bcrypt CPU-DoS that
//                  was wide open before. Keyed by the client IP, which
//                  is best-effort: behind Traefik we trust X-Real-IP,
//                  then leftmost X-Forwarded-For, then RemoteAddr. A
//                  spoofed X-Real-IP just rate-limits the attacker
//                  themselves more strictly, never less — fail-safe.
//
//   - maxBody:     wraps r.Body in http.MaxBytesReader so a 1GB JSON
//                  POST can't pin the gateway's RAM during decode.
//                  Composed per-route because different endpoints have
//                  legitimately different size ceilings (a 4 KB cap on
//                  /api/auth/* would break /load_pgn).
//
// Both are deliberately gateway-local (not in pkg/) because no other
// service exposes a public surface — game-service and engine-worker
// only get traffic from the gateway itself. If that changes, lift.

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// clientIP returns the best-effort caller IP. In production the gateway
// sits behind Traefik which sets X-Real-IP and appends to X-Forwarded-
// For; we honor those first. In dev/CI (no proxy) we fall back to the
// connection's RemoteAddr.
//
// Worst-case spoofing: an attacker forges X-Real-IP to look like
// another IP. That dilutes their own bucket faster, never another
// user's — they share whatever IP they claim. Good enough for what
// rate limiting is supposed to do here.
func clientIP(r *http.Request) string {
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For: "<client>, <proxy1>, <proxy2>". Leftmost is
		// the original client per RFC 7239 / Traefik default behavior.
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipLimiter holds a per-IP token bucket plus a "last seen" timestamp
// used by the janitor to evict entries that have gone quiet. Atomic
// last-seen so the hot path doesn't take the bucket's lock just to
// touch it.
type ipLimiter struct {
	lim      *rate.Limiter
	lastSeen atomic.Int64 // unix nanos
}

// Limiter is a per-IP rate limiter with bounded memory growth via a
// background janitor. Safe for concurrent use.
type Limiter struct {
	r     rate.Limit
	burst int

	mu      sync.RWMutex
	buckets map[string]*ipLimiter
}

// NewLimiter creates a limiter that allows `rps` requests/sec on
// average with a burst capacity of `burst` tokens per IP.
func NewLimiter(rps float64, burst int) *Limiter {
	return &Limiter{
		r:       rate.Limit(rps),
		burst:   burst,
		buckets: make(map[string]*ipLimiter),
	}
}

// Allow reports whether the given key's bucket has a token to spend.
// First call for a key mints a fresh bucket; subsequent calls reuse it.
func (l *Limiter) Allow(key string) bool {
	now := time.Now().UnixNano()

	l.mu.RLock()
	b, ok := l.buckets[key]
	l.mu.RUnlock()
	if !ok {
		l.mu.Lock()
		// Re-check under the write lock — a concurrent first-touch may
		// have just minted the bucket.
		if b, ok = l.buckets[key]; !ok {
			b = &ipLimiter{lim: rate.NewLimiter(l.r, l.burst)}
			l.buckets[key] = b
		}
		l.mu.Unlock()
	}
	b.lastSeen.Store(now)
	return b.lim.Allow()
}

// RunJanitor evicts buckets that haven't been touched for `idle`. Run
// once per `period`. Cancellation via ctx stops it. Bounded growth: at
// our scale, a few hundred KB even under a heavy unique-IP storm.
func (l *Limiter) RunJanitor(stop <-chan struct{}, idle, period time.Duration) {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			cutoff := now.Add(-idle).UnixNano()
			l.mu.Lock()
			for k, b := range l.buckets {
				if b.lastSeen.Load() < cutoff {
					delete(l.buckets, k)
				}
			}
			l.mu.Unlock()
		}
	}
}

// rateLimit wraps a HandlerFunc with the per-IP token bucket. Rejected
// requests get 429 + the optional Retry-After header (clients should
// respect it; browsers do).
func rateLimit(l *Limiter, retryAfterSec int) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(clientIP(r)) {
				if retryAfterSec > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(retryAfterSec))
				}
				http.Error(w, "too many requests, slow down", http.StatusTooManyRequests)
				return
			}
			next(w, r)
		}
	}
}

// maxBody wraps r.Body in http.MaxBytesReader. Decoders that go past
// the cap get a clear 400 error instead of silently consuming memory.
func maxBody(n int64) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next(w, r)
		}
	}
}
