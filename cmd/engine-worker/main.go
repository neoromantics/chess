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

	// Subscribe to engine abort signals
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

	go func() {
		<-sigChan
		log.Println("Shutting down worker...")
		cancel()
	}()

	// Worker Pool Semaphore
	maxWorkers := runtime.NumCPU()
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	sem := make(chan struct{}, maxWorkers)
	log.Printf("Worker pool initialized with %d slots", maxWorkers)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Dequeue a task (blocks until one is available)
			payload, err := eventBus.Dequeue(ctx, bus.EngineRequestChannel)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("Worker: failed to dequeue task: %v", err)
					time.Sleep(1 * time.Second)
				}
				continue
			}

			var req bus.EngineRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				log.Printf("Worker: failed to unmarshal request: %v", err)
				continue
			}

			// Acquire worker slot (blocks if at capacity, interruptible by ctx)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			log.Printf("Worker [ENGINE]: Processing request for ID: %s - Time: %v", req.GameID, req.MoveTime)

			go func(r bus.EngineRequest) {
				defer func() { <-sem }() // Release slot

				// Register active search
				stop := &atomic.Bool{}
				activeSearches.Store(r.GameID, stop)
				defer activeSearches.Delete(r.GameID)

				resp := ProcessRequest(r, stop)
				if err := eventBus.Publish(context.Background(), bus.EngineResponseChannel, resp); err != nil {
					log.Printf("Worker: failed to publish response: %v", err)
				}
				log.Printf("Worker [ENGINE]: Finished request for ID: %s - Move: %s", resp.GameID, resp.BestMove)
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
