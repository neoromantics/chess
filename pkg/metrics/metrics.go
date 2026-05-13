// Package metrics is the shared Prometheus instrumentation surface for
// every service in this repo. Each service main() should:
//
//	mux := http.NewServeMux()
//	// register handlers...
//	mux.Handle("/metrics", metrics.Handler())
//	http.ListenAndServe(addr, metrics.HTTPMiddleware("svc-name", mux))
//
// All services share label cardinality discipline: routes are
// templated (e.g. "/api/invites/{id}/accept" not the resolved UUID)
// so the metric series count stays bounded.
//
// Once a Prometheus instance is running, scrape via the
// prometheus.io/scrape=true and prometheus.io/port=<port> annotations
// on each Service (see infra/deploy.yaml).
package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chess_http_requests_total",
		Help: "Total HTTP requests served, partitioned by method/route/status.",
	}, []string{"service", "method", "route", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "chess_http_request_duration_seconds",
		Help:    "HTTP request latency distribution. The 'route' label is the templated path, not the resolved one — series cardinality stays bounded.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 16), // 1ms .. ~32s
	}, []string{"service", "method", "route"})

	WSConnectionsActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "chess_ws_connections_active",
		Help: "Live WebSocket connection count, by channel kind (game/user). Future HPA candidate for chess-gateway.",
	}, []string{"service", "kind"})

	// Stream-depth scrape target for KEDA / dashboards. Updated by a
	// small periodic loop in the consumer service (see eg. game-service
	// startup); kept here so the metric name + help string live in one
	// place.
	RedisStreamPending = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "chess_redis_stream_pending_count",
		Help: "Number of unacked messages in a Redis stream's consumer group. Real autoscale signal for engine-worker when KEDA lands.",
	}, []string{"service", "stream", "group"})
)

// Handler exposes /metrics. Use it directly in any mux.
func Handler() http.Handler { return promhttp.Handler() }

// HTTPMiddleware records request count + latency per route. The route
// label is derived from the matched http.Request.Pattern (Go 1.22+
// enhanced ServeMux populates this), falling back to a templatized
// version of the URL path that strips dynamic-looking segments so
// /api/invites/<uuid>/accept becomes /api/invites/:id/accept rather
// than minting a new metric series per UUID.
func HTTPMiddleware(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := r.Pattern
		if route == "" {
			route = templatize(r.URL.Path)
		}
		dur := time.Since(start).Seconds()
		HTTPRequestsTotal.WithLabelValues(service, r.Method, route, strconv.Itoa(rec.status)).Inc()
		HTTPRequestDuration.WithLabelValues(service, r.Method, route).Observe(dur)
	})
}

// statusRecorder is a minimal http.ResponseWriter wrapper so the
// middleware can see what status code the handler chose. Avoids
// depending on a fuller library.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// templatize replaces obvious dynamic segments with :id. Best-effort
// fallback for handlers registered without a Go-1.22 Pattern.
func templatize(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if isLikelyDynamic(seg) {
			parts[i] = ":id"
		}
	}
	return strings.Join(parts, "/")
}

// isLikelyDynamic guesses if a path segment is a UUID / int / etc. so
// we collapse it to :id in the metric label. Conservative: only matches
// segments that are obviously not route literals.
func isLikelyDynamic(seg string) bool {
	if len(seg) == 36 && strings.Count(seg, "-") == 4 {
		return true // UUID
	}
	if len(seg) > 0 && seg[0] >= '0' && seg[0] <= '9' {
		return true // int-ish
	}
	return false
}
