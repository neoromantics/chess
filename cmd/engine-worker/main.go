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

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/uci"
	"golang.org/x/term"
)

var (
	activeSearches sync.Map // game_id -> *atomic.Bool
	uciMode        = flag.Bool("uci", false, "Run in pure UCI mode (CLI)")
)

func main() {
	flag.Parse()

	// 1. Check for UCI mode (explicit flag or piped input)
	if *uciMode || !term.IsTerminal(int(os.Stdin.Fd())) {
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

	go func() {
		<-sigChan
		log.Println("Shutting down worker...")
		cancel()
	}()

	for {
		select {
		case <-ctx.Done():
			return
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

			go func(r eventbus.EngineRequest, streamID string) {
				defer func() { <-sem }()
				defer bus.Ack(context.Background(), eventbus.StreamEngineRequests, "engine-worker-group", streamID)

				log.Printf("Worker [ENGINE]: Processing %s request for Game %s (ID: %s)", r.Context, r.GameID, streamID)

				stop := &atomic.Bool{}
				activeSearches.Store(r.GameID, stop)
				defer activeSearches.Delete(r.GameID)

				resp := ProcessRequest(r, stop)
				if err := bus.SendEngineResponse(context.Background(), resp); err != nil {
					log.Printf("Worker: failed to publish response: %v", err)
				}
				log.Printf("Worker [ENGINE]: Finished %s request for Game %s", r.Context, r.GameID)
			}(req, msg.ID)
		}
	}
}

// ProcessRequest runs the engine on the given position.
func ProcessRequest(req eventbus.EngineRequest, stop *atomic.Bool) eventbus.EngineResponse {
	b, err := core.ParseFEN(req.FEN)
	if err != nil {
		return eventbus.EngineResponse{GameID: req.GameID, Context: req.Context, Metadata: req.Metadata}
	}

	if req.Context == "assess" {
		userMoveStr := req.Metadata["move"]
		m, err := b.ParseUCIMove(userMoveStr)
		if err != nil {
			return eventbus.EngineResponse{GameID: req.GameID, Context: req.Context, Metadata: req.Metadata}
		}

		resBest := b.IterativeDeepening(core.SearchLimits{MoveTime: req.MoveTime, History: req.History}, stop, nil)
		bAfter := *b
		bAfter.MakeMove(m)
		resUser := bAfter.IterativeDeepening(core.SearchLimits{MoveTime: req.MoveTime, History: req.History}, stop, nil)

		resp := eventbus.EngineResponse{
			GameID:   req.GameID,
			BestMove: resBest.BestMove.String(),
			Score:    resBest.Score,
			Depth:    resBest.Depth,
			Context:  req.Context,
			Metadata: make(map[string]string),
		}
		for k, v := range req.Metadata {
			resp.Metadata[k] = v
		}
		resp.Metadata["best_score"] = fmt.Sprintf("%d", resBest.Score)
		resp.Metadata["user_score"] = fmt.Sprintf("%d", -resUser.Score)
		resp.Metadata["player_side"] = fmt.Sprintf("%v", b.SideToMove)
		return resp
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
