package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/metrics"
	"github.com/neoromantics/chess/pkg/uci"
)

var (
	activeSearches sync.Map // game_id -> *atomic.Bool
	uciMode        = flag.Bool("uci", false, "Run in pure UCI mode (CLI)")
)

func main() {
	flag.Parse()

	// UCI mode is opt-in via -uci. Auto-detecting via tty heuristic
	// (term.IsTerminal(stdin)) was a footgun in containers — k8s pods
	// have no tty, so the worker would silently enter UCI mode on
	// startup, read stdin, hit EOF, exit clean. K8s saw Completed,
	// restarted, repeat forever.
	if *uciMode {
		u := uci.NewUCI(os.Stdout)
		u.Run(os.Stdin)
		return
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = fmt.Sprintf("worker-%d", os.Getpid())
	}

	log.Printf("Engine Worker [%s] starting...", hostname)

	redisAddr := os.Getenv("REDIS_URL")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	bus := eventbus.NewClient(redisAddr)
	defer bus.Close()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	maxConcurrent := runtime.GOMAXPROCS(0)
	if v := os.Getenv("WORKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxConcurrent = n
		}
	}
	sem := make(chan struct{}, maxConcurrent)
	log.Printf("Worker [ENGINE]: Bounded concurrency to %d parallel searches", maxConcurrent)

	// Tiny HTTP server for /metrics + /health probes. Worker is otherwise
	// a pure stream consumer with no inbound traffic.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("OK"))
	})
	mux.Handle("/metrics", metrics.Handler())
	srv := &http.Server{
		Addr:    ":8080",
		Handler: metrics.HTTPMiddleware("engine-worker", mux),
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Worker: HTTP server failed: %v", err)
		}
	}()

	// inFlight tracks goroutines spawned per accepted search. On
	// shutdown we wait for them to finish their current search and ack
	// before exiting, so the consumer group's pending-entries list
	// doesn't grow with orphaned messages that the next pod's pendingMS
	// reclaim has to mop up.
	var inFlight sync.WaitGroup

	go func() {
		<-sigChan
		log.Println("Shutting down worker...")
		cancel()
	}()

readLoop:
	for {
		select {
		case <-ctx.Done():
			break readLoop
		case sem <- struct{}{}:
			msgs, err := bus.ReadEngineRequests(ctx, "engine-worker-group", hostname, 5*time.Second)
			if err != nil {
				<-sem
				if ctx.Err() == nil {
					log.Printf("Worker: read error: %v", err)
				}
				continue
			}

			if len(msgs) == 0 {
				<-sem
				continue
			}

			msg := msgs[0]
			var req eventbus.EngineRequest
			data, ok := msg.Values["data"].(string)
			if !ok {
				<-sem
				continue
			}
			if err := json.Unmarshal([]byte(data), &req); err != nil {
				<-sem
				log.Printf("Worker: failed to unmarshal request: %v", err)
				bus.Ack(ctx, eventbus.StreamEngineRequests, "engine-worker-group", msg.ID)
				continue
			}

			inFlight.Add(1)
			go func(r eventbus.EngineRequest, streamID string) {
				defer inFlight.Done()
				defer func() { <-sem }()
				defer bus.Ack(context.Background(), eventbus.StreamEngineRequests, "engine-worker-group", streamID)

				log.Printf("Worker [ENGINE]: Processing %s request for Game %s (ID: %s)", r.Context, r.GameID, streamID)

				stop := &atomic.Bool{}
				activeSearches.Store(r.GameID, stop)
				defer activeSearches.Delete(r.GameID)

				// Wall-clock latency per search, labeled by context so
				// /analyze (assess) doesn't pollute the per-move p95.
				t0 := time.Now()
				resp := ProcessRequest(r, stop)
				metrics.EngineSearchDuration.WithLabelValues(r.Context).Observe(time.Since(t0).Seconds())

				if err := bus.SendEngineResponse(context.Background(), resp); err != nil {
					log.Printf("Worker: failed to publish response: %v", err)
				}
				log.Printf("Worker [ENGINE]: Finished %s request for Game %s", r.Context, r.GameID)
			}(req, msg.ID)
		}
	}

	// Signal every active search to abandon its remaining depth and
	// return whatever best-move it already has. Without this the worker
	// pod sits in Terminating until kube SIGKILLs it.
	activeSearches.Range(func(_, v any) bool {
		if stop, ok := v.(*atomic.Bool); ok {
			stop.Store(true)
		}
		return true
	})

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Worker: HTTP shutdown error: %v", err)
	}
	inFlight.Wait()
	log.Println("Worker: clean shutdown complete")
}

// ProcessRequest runs the engine on the given position.
func ProcessRequest(req eventbus.EngineRequest, stop *atomic.Bool) eventbus.EngineResponse {
	b, err := core.ParseFEN(req.FEN)
	if err != nil {
		return eventbus.EngineResponse{GameID: req.GameID, Context: req.Context, Metadata: req.Metadata}
	}

	res := b.IterativeDeepening(core.SearchLimits{
		MoveTime: req.MoveTime,
		History:  req.History,
	}, stop, nil)

	return eventbus.EngineResponse{
		GameID:   req.GameID,
		BestMove: res.BestMove.String(),
		Score:    res.Score,
		Depth:    res.Depth,
		Context:  req.Context,
		Metadata: req.Metadata,
	}
}
