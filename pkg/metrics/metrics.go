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
	"bufio"
	"errors"
	"net"
	"net/http"
	"strconv"
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

	// ===== Business metrics =====
	// HTTP latency + WS counts tell us "how fast is the gateway"; these
	// tell us "how is the game system doing." Without them, a degraded
	// engine-worker or a stuck matchmaker only surfaces via customer
	// complaints.

	// MovesAppliedTotal counts moves that successfully advanced a game
	// row. Labeled by source so engine-driven moves can be split from
	// human moves at query time.
	MovesAppliedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chess_moves_applied_total",
		Help: "Total moves successfully applied to a game record. source=http for SPA-driven moves, source=engine for engine-result moves.",
	}, []string{"source"})

	// EngineSearchDuration measures how long ProcessRequest takes in
	// engine-worker. Context distinguishes move/hint/assess so an
	// /analyze burst doesn't pollute the per-move p95.
	EngineSearchDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "chess_engine_search_duration_seconds",
		Help:    "Engine search wall time per ProcessRequest, by request context (move/hint/assess).",
		Buckets: prometheus.ExponentialBuckets(0.01, 2, 12), // 10ms .. ~40s
	}, []string{"context"})

	// MatchmakerQueueDepth is the live ZCARD of each per-TC queue.
	// Set once per pair-tick by the leader-elected matchmaker loop.
	MatchmakerQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "chess_matchmaker_queue_depth",
		Help: "Players currently waiting in each time-control queue. Set per pair-tick by the matchmaker leader.",
	}, []string{"time_control"})

	// MatchmakerWaitSeconds is the wait between joining the queue and
	// either being paired or falling through to the engine. kind splits
	// real pairings from the engine-fallback path.
	MatchmakerWaitSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "chess_matchmaker_wait_seconds",
		Help:    "Wait between join-queue and dispatch, by time control and kind (paired|engine_fallback).",
		Buckets: prometheus.ExponentialBuckets(1, 2, 10), // 1s .. ~17min
	}, []string{"time_control", "kind"})

	// GamesFinishedTotal counts games that reached a terminal status.
	// Labeled by result + rated so a Grafana panel can split rated
	// wins/losses/draws from casual play.
	GamesFinishedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chess_games_finished_total",
		Help: "Total games reaching a terminal status (checkmate/stalemate/resign/timeout/agreed/draw).",
	}, []string{"result", "rated"})

	// RatingUpdateDuration tracks how long the Glicko-2 update takes
	// per finished rated game. Pure CPU + 2 PG updates; if it ever
	// runs slow, suspect the DB.
	RatingUpdateDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "chess_rating_update_duration_seconds",
		Help:    "Wall time for one Glicko-2 update pair (both players) after a finished rated game.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 12), // 1ms .. ~4s
	})
)

// Handler exposes /metrics. Use it directly in any mux.
func Handler() http.Handler { return promhttp.Handler() }

// HTTPMiddleware records request count + latency per route. The route
// label is the matched http.Request.Pattern (Go 1.22+ enhanced ServeMux
// populates this for both "METHOD /path/{id}" and prefix patterns like
// "/api/invites/"). Requests that fall through without a Pattern are
// bucketed as "<unknown>" — guarantees bounded cardinality, and a spike
// in that label on the dashboard is the loud signal that a new handler
// was registered without a Method+Pattern declaration.
func HTTPMiddleware(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := r.Pattern
		if route == "" {
			route = "<unknown>"
		}
		dur := time.Since(start).Seconds()
		HTTPRequestsTotal.WithLabelValues(service, r.Method, route, strconv.Itoa(rec.status)).Inc()
		HTTPRequestDuration.WithLabelValues(service, r.Method, route).Observe(dur)
	})
}

// statusRecorder is a minimal http.ResponseWriter wrapper so the
// middleware can see what status code the handler chose. Avoids
// depending on a fuller library.
//
// CRITICAL: a wrapper that embeds http.ResponseWriter only exposes
// methods on the interface, NOT extra methods the concrete underlying
// implementation has. Real Go HTTP responses also implement
// http.Hijacker (gorilla/websocket needs it to take the underlying
// connection) and http.Flusher (SSE / streaming need it). Forgetting
// to forward Hijack() broke every WebSocket upgrade in the cluster
// — symptoms: 'websocket: response does not implement http.Hijacker'
// in logs and silent connection failures in the SPA. Don't remove
// these forward methods.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack forwards to the underlying ResponseWriter so gorilla/websocket
// can take over the connection during an Upgrade.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}

// Flush forwards to the underlying ResponseWriter for SSE / streaming
// handlers. Not used today, but cheap insurance against the next
// "middleware ate my streaming response" bug.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
