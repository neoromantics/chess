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
	"github.com/neoromantics/chess/pkg/leader"
)

var activeSearches sync.Map // game_id -> *atomic.Bool

func main() {
	flag.Parse()

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = fmt.Sprintf("worker-%d", os.Getpid())
	}

	log.Printf("Engine Worker [%s] starting...", hostname)

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

	// 1. Leader-elected janitor for Redis Streams (XCLAIM stale tasks)
	startJanitor(ctx, eventBus, hostname)

	// 2. Event Subscriptions (Pub/Sub)
	eventBus.Subscribe(ctx, bus.GameFinishedEventChannel, func(payload []byte) {
		var event bus.GameFinishedEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return
		}
		log.Printf("Worker [ARCHIVE]: Received GAME_FINISHED event for ID: %s", event.GameID)
	})

	eventBus.Subscribe(ctx, bus.EngineAbortChannel, func(payload []byte) {
		var abort bus.EngineAbort
		if err := json.Unmarshal(payload, &abort); err != nil {
			return
		}
		if stop, ok := activeSearches.Load(abort.GameID); ok {
			log.Printf("Worker [ENGINE]: Aborting search for ID: %s", abort.GameID)
			stop.(*atomic.Bool).Store(true)
		}
	})

	// 3. Main Stream Consumer Loop
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
			// 1. First try to read NEW messages (never delivered)
			msgs, err := eventBus.StreamReadGroup(ctx, bus.EngineRequestChannel, "engine-workers", hostname, 2*time.Second)
			if err != nil {
				<-sem
				if ctx.Err() == nil {
					log.Printf("Worker: stream read error: %v", err)
				}
				continue
			}

			// 2. If no new messages, try to read PENDING messages (delivered but not ACKed, or claimed by Janitor)
			if len(msgs) == 0 {
				msgs, err = eventBus.StreamReadGroupPending(ctx, bus.EngineRequestChannel, "engine-workers", hostname, 10)
				if err != nil || len(msgs) == 0 {
					<-sem
					continue
				}
			}

			msg := msgs[0]
			var req bus.EngineRequest
			if err := json.Unmarshal([]byte(msg.Values["data"].(string)), &req); err != nil {
				<-sem
				log.Printf("Worker: failed to unmarshal request: %v", err)
				eventBus.StreamAck(ctx, bus.EngineRequestChannel, "engine-workers", msg.ID)
				continue
			}

			go func(r bus.EngineRequest, streamID string) {
				defer func() { <-sem }()
				defer eventBus.StreamAck(context.Background(), bus.EngineRequestChannel, "engine-workers", streamID)

				log.Printf("Worker [ENGINE]: Processing %s request for Game %s (ID: %s)", r.Context, r.GameID, streamID)

				stop := &atomic.Bool{}
				activeSearches.Store(r.GameID, stop)
				defer activeSearches.Delete(r.GameID)

				resp := ProcessRequest(r, stop)
				if err := eventBus.Publish(context.Background(), bus.EngineResponseChannel, resp); err != nil {
					log.Printf("Worker: failed to publish response: %v", err)
				}
				log.Printf("Worker [ENGINE]: Finished %s request for Game %s", r.Context, r.GameID)
			}(req, msg.ID)
		}
	}
	}

func startJanitor(ctx context.Context, b *bus.Client, hostname string) {
	election := leader.NewElection(b.Rdb(), "worker-janitor", leader.WithLeaseTTL(15*time.Second))
	go election.Run(ctx, func(leaderCtx context.Context) {
		log.Println("Worker Janitor: assumed leadership")
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-leaderCtx.Done():
				return
			case <-ticker.C:
				// XCLAIM jobs pending for > 60s
				pending, err := b.StreamPending(leaderCtx, bus.EngineRequestChannel, "engine-workers", 10)
				if err != nil {
					continue
				}
				var staleIDs []string
				for _, p := range pending {
					if p.Idle > 60*time.Second {
						staleIDs = append(staleIDs, p.ID)
					}
				}
				if len(staleIDs) > 0 {
					log.Printf("Worker Janitor: claiming %d stale tasks", len(staleIDs))
					b.StreamClaim(leaderCtx, bus.EngineRequestChannel, "engine-workers", hostname, 60*time.Second, staleIDs)
				}
			}
		}
	})
}

// ProcessRequest runs the engine on the given position.
func ProcessRequest(req bus.EngineRequest, stop *atomic.Bool) bus.EngineResponse {
	b, err := core.ParseFEN(req.FEN)
	if err != nil {
		return bus.EngineResponse{GameID: req.GameID, Context: req.Context, Metadata: req.Metadata}
	}

	if req.Context == "assess" {
		userMoveStr := req.Metadata["move"]
		m, err := b.ParseUCIMove(userMoveStr)
		if err != nil {
			return bus.EngineResponse{GameID: req.GameID, Context: req.Context, Metadata: req.Metadata}
		}

		resBest := b.IterativeDeepening(core.SearchLimits{MoveTime: req.MoveTime, History: req.History}, stop, nil)
		bAfter := *b
		bAfter.MakeMove(m)
		resUser := bAfter.IterativeDeepening(core.SearchLimits{MoveTime: req.MoveTime, History: req.History}, stop, nil)

		resp := bus.EngineResponse{
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

	return bus.EngineResponse{
		GameID:   req.GameID,
		BestMove: res.BestMove.String(),
		Score:    res.Score,
		Depth:    res.Depth,
		Context:  req.Context,
		Metadata: req.Metadata,
	}
}
