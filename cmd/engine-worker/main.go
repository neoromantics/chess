package main

import (
	"context"
	"encoding/json"
	"flag"
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
		return bus.EngineResponse{GameID: req.GameID, Context: req.Context}
	}

	stop := &atomic.Bool{}
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
	}
}
