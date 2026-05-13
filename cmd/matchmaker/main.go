package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/metrics"
	"github.com/redis/go-redis/v9"
)

type Matchmaker struct {
	bus *eventbus.Client
}

func main() {
	redisAddr := os.Getenv("REDIS_URL")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	bus := eventbus.NewClient(redisAddr)
	s := &Matchmaker{bus: bus}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	// Health check server
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte("OK"))
		})
		mux.Handle("/metrics", metrics.Handler())
		http.ListenAndServe(":8080", metrics.HTTPMiddleware("matchmaker", mux))
	}()

	slog.Info("Matchmaker Service starting...")

	// Start pairing loop
	go s.pairingLoop(ctx)

	s.Run(ctx)
}

func (s *Matchmaker) Run(ctx context.Context) {
	hostname, _ := os.Hostname()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			msgs, err := s.bus.ReadCommands(ctx, "matchmaker-group", hostname, 5*time.Second)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("read commands error", "error", err)
				}
				time.Sleep(1 * time.Second)
				continue
			}

			for _, msg := range msgs {
				s.processCommand(ctx, msg)
				s.bus.Ack(ctx, eventbus.StreamGameCommands, "matchmaker-group", msg.ID)
			}
		}
	}
}

func (s *Matchmaker) processCommand(ctx context.Context, msg redis.XMessage) {
	var cmd eventbus.Command
	data, ok := msg.Values["data"].(string)
	if !ok {
		return
	}
	if err := json.Unmarshal([]byte(data), &cmd); err != nil {
		return
	}

	switch cmd.Type {
	case eventbus.CmdJoinQueue:
		s.handleJoinQueue(ctx, cmd)
	case eventbus.CmdLeaveQueue:
		s.handleLeaveQueue(ctx, cmd)
	}
}

func (s *Matchmaker) handleJoinQueue(ctx context.Context, cmd eventbus.Command) {
	var payload eventbus.JoinQueueCmd
	json.Unmarshal(cmd.Payload, &payload)

	key := "mm:queue:" + payload.TimeControl
	s.bus.Rdb().ZAdd(ctx, key, redis.Z{
		Score:  float64(payload.Rating),
		Member: fmt.Sprintf("%d", cmd.UserID),
	})
	slog.Info("user joined queue", "user_id", cmd.UserID, "tc", payload.TimeControl)
}

func (s *Matchmaker) handleLeaveQueue(ctx context.Context, cmd eventbus.Command) {
	var payload eventbus.LeaveQueueCmd
	json.Unmarshal(cmd.Payload, &payload)

	key := "mm:queue:" + payload.TimeControl
	s.bus.Rdb().ZRem(ctx, key, fmt.Sprintf("%d", cmd.UserID))
	slog.Info("user left queue", "user_id", cmd.UserID, "tc", payload.TimeControl)
}

func (s *Matchmaker) pairingLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	tcs := []string{"1+0", "3+2", "10+5"}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, tc := range tcs {
				s.tryPair(ctx, tc)
			}
		}
	}
}

func (s *Matchmaker) tryPair(ctx context.Context, tc string) {
	key := "mm:queue:" + tc
	// Simple pairing: take first 2
	res, err := s.bus.Rdb().ZRange(ctx, key, 0, 1).Result()
	if err != nil || len(res) < 2 {
		return
	}

	u1, _ := strconv.ParseInt(res[0], 10, 64)
	u2, _ := strconv.ParseInt(res[1], 10, 64)

	// Remove from queue
	s.bus.Rdb().ZRem(ctx, key, res[0], res[1])

	// Create match
	gameID := uuid.New().String()
	slog.Info("match found", "u1", u1, "u2", u2, "game_id", gameID)

	// Emit MatchFound event
	evtPayload, _ := json.Marshal(eventbus.MatchFoundEvt{
		GameID:      gameID,
		WhiteUserID: u1,
		BlackUserID: u2,
	})

	// Dispatch game creation to Game Service
	createPayload, _ := json.Marshal(eventbus.CreatePvPGameCmd{
		WhiteUserID: u1,
		BlackUserID: u2,
		TimeControl: tc,
		Rated:       true,
	})
	s.bus.SendCommand(ctx, eventbus.Command{
		Type:    eventbus.CmdCreatePvPGame,
		GameID:  gameID,
		Payload: createPayload,
	})

	s.bus.EmitEvent(ctx, eventbus.Event{
		Type:    eventbus.EvtMatchFound,
		GameID:  gameID,
		Payload: evtPayload,
	})

	// Also push targeted events to users for frontend redirection
	s.bus.PublishUserEvent(ctx, u1, eventbus.Event{Type: eventbus.EvtMatchFound, GameID: gameID})
	s.bus.PublishUserEvent(ctx, u2, eventbus.Event{Type: eventbus.EvtMatchFound, GameID: gameID})
}
