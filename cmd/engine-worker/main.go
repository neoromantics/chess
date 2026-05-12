package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/taiyanliu/chess/pkg/core"
)

// Job represents a move calculation request.
type Job struct {
	GameID   string            `json:"game_id"`
	FEN      string            `json:"fen"`
	History  map[uint64]int    `json:"history"`
	MoveTime time.Duration     `json:"movetime"`
}

// Result represents the calculated best move.
type Result struct {
	GameID   string          `json:"game_id"`
	BestMove core.Move       `json:"best_move"`
	Score    int             `json:"score"`
	Depth    int             `json:"depth"`
	Error    string          `json:"error,omitempty"`
}

func main() {
	// In a million-dollar business, this would subscribe to Redis/RabbitMQ.
	// For now, we'll implement the structure to be easily swappable.
	flag.Parse()

	log.Println("Engine Worker starting...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Simulate worker loop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
			// In the future: job := queue.Pop()
			time.Sleep(1 * time.Second)
		}
	}
}

// ProcessJob runs the engine on the given position.
func ProcessJob(job Job) Result {
	b, err := core.ParseFEN(job.FEN)
	if err != nil {
		return Result{GameID: job.GameID, Error: err.Error()}
	}

	stop := &atomic.Bool{}
	res := b.IterativeDeepening(core.SearchLimits{
		MoveTime: job.MoveTime,
		History:  job.History,
	}, stop, nil)

	return Result{
		GameID:   job.GameID,
		BestMove: res.BestMove,
		Score:    res.Score,
		Depth:    res.Depth,
	}
}
