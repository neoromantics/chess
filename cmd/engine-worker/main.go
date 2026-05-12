package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/neoromantics/chess/pkg/bus"
	"github.com/neoromantics/chess/pkg/core"
)

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

	// Subscribe to engine calculation requests
	eventBus.Subscribe(ctx, bus.EngineRequestChannel, func(payload []byte) {
		var req bus.EngineRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			log.Printf("Worker: failed to unmarshal request: %v", err)
			return
		}

		log.Printf("Worker [ENGINE]: Received calculation request for ID: %s - Time: %v", req.GameID, req.MoveTime)
		
		// Run calculation in a goroutine to allow parallel processing
		go func(r bus.EngineRequest) {
			resp := ProcessRequest(r)
			if err := eventBus.Publish(context.Background(), bus.EngineResponseChannel, resp); err != nil {
				log.Printf("Worker: failed to publish response: %v", err)
			}
			log.Printf("Worker [ENGINE]: Published response for ID: %s - Move: %s", resp.GameID, resp.BestMove)
		}(req)
	})

	go func() {
		<-sigChan
		log.Println("Shutting down worker...")
		cancel()
	}()

	workerLoop(ctx)
}

func workerLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Main loop keep-alive
			time.Sleep(1 * time.Second)
		}
	}
}

// ProcessRequest runs the engine on the given position.
func ProcessRequest(req bus.EngineRequest) bus.EngineResponse {
	b, err := core.ParseFEN(req.FEN)
	if err != nil {
		log.Printf("Worker: failed to parse FEN: %v", err)
		return bus.EngineResponse{GameID: req.GameID, Context: req.Context, Metadata: req.Metadata}
	}

	stop := &atomic.Bool{}
	
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
		for k, v := range req.Metadata { resp.Metadata[k] = v }
		
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
