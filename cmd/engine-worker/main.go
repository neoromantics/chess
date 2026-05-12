package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/neoromantics/chess/pkg/bus"
	"github.com/neoromantics/chess/pkg/core"
)

var activeSearches sync.Map // game_id -> *atomic.Bool

func main() {
	flag.Parse()

	log.Println("Engine Worker starting...")

	redisAddr := os.Getenv("REDIS_URL")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	eventBus := bus.NewClient(redisAddr)
	defer eventBus.Close()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe to game finished events (Archive/Rating placeholder)
	eventBus.Subscribe(ctx, bus.GameFinishedEventChannel, func(payload []byte) {
		var event bus.GameFinishedEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Printf("Worker: failed to unmarshal event: %v", err)
			return
		}
		log.Printf("Worker [ARCHIVE/RATING]: Received GAME_FINISHED event for ID: %s - Result: %s", event.GameID, event.Status)
	})

	// Subscribe to engine abort signals (still Pub/Sub — all workers must hear aborts)
	eventBus.Subscribe(ctx, bus.EngineAbortChannel, func(payload []byte) {
		var abort bus.EngineAbort
		if err := json.Unmarshal(payload, &abort); err != nil {
			log.Printf("Worker: failed to unmarshal abort: %v", err)
			return
		}

		if stop, ok := activeSearches.Load(abort.GameID); ok {
			log.Printf("Worker [ENGINE]: Aborting search for ID: %s", abort.GameID)
			stop.(*atomic.Bool).Store(true)
		}
	})

	// CPU-bounded semaphore: limits concurrent engine searches to the
	// container's effective core budget. runtime.NumCPU() returns the host
	// CPU count and IGNORES cgroup CPU limits — on a 16-core node with
	// cpu.limit=1, that would spawn 16 parallel searches all fighting for
	// 6% of one core. GOMAXPROCS(0) is cgroup-aware since Go 1.22, so it
	// reflects the actual scheduler parallelism we're allowed.
	//
	// Override with WORKER_CONCURRENCY env when running ad-hoc or
	// experimenting with oversubscription.
	maxConcurrent := runtime.GOMAXPROCS(0)
	if v := os.Getenv("WORKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxConcurrent = n
		}
	}
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	sem := make(chan struct{}, maxConcurrent)
	log.Printf("Worker [ENGINE]: Bounded concurrency to %d parallel searches (GOMAXPROCS=%d, NumCPU=%d)",
		maxConcurrent, runtime.GOMAXPROCS(0), runtime.NumCPU())

	go func() {
		<-sigChan
		log.Println("Shutting down worker...")
		cancel()
	}()

	// Main dequeue loop: pulls tasks from the Redis List one at a time.
	// BLPOP ensures exactly-once delivery — only this worker gets this task.
	for {
		select {
		case <-ctx.Done():
			log.Println("Worker: context cancelled, draining active searches...")
			// Wait for all active searches to finish
			for i := 0; i < maxConcurrent; i++ {
				sem <- struct{}{}
			}
			log.Println("Worker: graceful shutdown complete")
			return
		case sem <- struct{}{}: // Acquire semaphore slot before dequeuing
			payload, err := eventBus.Dequeue(ctx, bus.EngineRequestChannel, 2*time.Second)
			if err != nil {
				<-sem // Release slot on error
				if ctx.Err() != nil {
					return
				}
				// BLPOP timeout — just loop again
				continue
			}

			var req bus.EngineRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				<-sem
				log.Printf("Worker: failed to unmarshal request: %v", err)
				continue
			}

			log.Printf("Worker [ENGINE]: Dequeued task for ID: %s - Context: %s - Time: %v", req.GameID, req.Context, req.MoveTime)

			go func(r bus.EngineRequest) {
				defer func() { <-sem }() // Release semaphore when done

				// Register active search
				stop := &atomic.Bool{}
				activeSearches.Store(r.GameID, stop)
				defer activeSearches.Delete(r.GameID)

				resp := ProcessRequest(r, stop)
				if err := eventBus.Publish(context.Background(), bus.EngineResponseChannel, resp); err != nil {
					log.Printf("Worker: failed to publish response: %v", err)
				}
				log.Printf("Worker [ENGINE]: Published response for ID: %s - Move: %s", resp.GameID, resp.BestMove)
			}(req)
		}
	}
}

// ProcessRequest runs the engine on the given position.
func ProcessRequest(req bus.EngineRequest, stop *atomic.Bool) bus.EngineResponse {
	b, err := core.ParseFEN(req.FEN)
	if err != nil {
		log.Printf("Worker: failed to parse FEN: %v", err)
		return bus.EngineResponse{GameID: req.GameID, Context: req.Context, Metadata: req.Metadata}
	}

	if req.Context == "assess" {
		userMoveStr := req.Metadata["move"]
		m, err := b.ParseUCIMove(userMoveStr)
		if err != nil {
			return bus.EngineResponse{GameID: req.GameID, Context: req.Context, Metadata: req.Metadata}
		}

		// 1. Search for best move
		resBest := b.IterativeDeepening(core.SearchLimits{MoveTime: req.MoveTime, History: req.History}, stop, nil)

		// 2. Evaluate user's move
		// We make the move on a copy and search to get a reliable score
		bAfter := *b
		bAfter.MakeMove(m)
		// Search depth should match roughly for fair comparison
		resUser := bAfter.IterativeDeepening(core.SearchLimits{MoveTime: req.MoveTime, History: req.History}, stop, nil)

		// In chess engine, score is relative to side to move.
		// For CP loss we need absolute scores or relative to the player who moved.
		// ProcessRequest receives b before userMove, so b.SideToMove is the player.

		resp := bus.EngineResponse{
			GameID:   req.GameID,
			BestMove: resBest.BestMove.String(),
			Score:    resBest.Score, // Engine's best score
			Depth:    resBest.Depth,
			Context:  req.Context,
			Metadata: make(map[string]string),
		}
		for k, v := range req.Metadata {
			resp.Metadata[k] = v
		}

		// Add results to metadata
		resp.Metadata["best_score"] = fmt.Sprintf("%d", resBest.Score)
		resp.Metadata["user_score"] = fmt.Sprintf("%d", -resUser.Score) // Flip because it's opponent's turn after move
		resp.Metadata["player_side"] = fmt.Sprintf("%v", b.SideToMove)

		return resp
	}

	// Default (move or hint)
	res := b.IterativeDeepening(core.SearchLimits{
		MoveTime: req.MoveTime,
		History:  req.History,
	}, stop, nil)

	return bus.EngineResponse{
		GameID:   req.GameID,
		BestMove: res.BestMove.String(),
		Score:    res.Score,
		Depth:    res.Depth,
		Context:  req.Context,
		Metadata: req.Metadata,
	}
}
