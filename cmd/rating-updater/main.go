package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/metrics"
	"github.com/neoromantics/chess/pkg/rating"
)

type RatingUpdater struct {
	db  db.Store
	bus *eventbus.Client
}

func main() {
	redisAddr := os.Getenv("REDIS_URL")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	store, err := db.OpenPostgres(dbURL)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	defer store.Close()

	bus := eventbus.NewClient(redisAddr)
	s := &RatingUpdater{db: store, bus: bus}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	// Small HTTP server that exists only to expose /metrics + /health
	// for k8s probes and Prometheus scrape. Rating updater is otherwise
	// a pure stream consumer.
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte("OK"))
		})
		mux.Handle("/metrics", metrics.Handler())
		_ = http.ListenAndServe(":8080", metrics.HTTPMiddleware("rating-updater", mux))
	}()

	slog.Info("Rating Updater starting...")
	s.Run(ctx)
}

func (s *RatingUpdater) Run(ctx context.Context) {
	hostname, _ := os.Hostname()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			msgs, err := s.bus.ReadEvents(ctx, "rating-updater-group", hostname, 5*time.Second)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("read events error", "error", err)
				}
				time.Sleep(1 * time.Second)
				continue
			}

			for _, msg := range msgs {
				s.processEvent(ctx, msg)
				s.bus.Ack(ctx, eventbus.StreamGameEvents, "rating-updater-group", msg.ID)
			}
		}
	}
}

func (s *RatingUpdater) processEvent(ctx context.Context, msg any) {
	// Re-typed for convenience
	m := msg.(struct {
		ID     string
		Values map[string]any
	})

	var evt eventbus.Event
	data, ok := m.Values["data"].(string)
	if !ok {
		return
	}
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return
	}

	if evt.Type == eventbus.EvtGameFinished {
		s.handleGameFinished(ctx, evt)
	}
}

func (s *RatingUpdater) handleGameFinished(ctx context.Context, evt eventbus.Event) {
	// 1. Fetch game details
	rec, err := s.db.GetGame(evt.GameID)
	if err != nil || !rec.Rated || rec.WhiteUserID == nil || rec.BlackUserID == nil {
		return
	}

	// 2. Fetch users
	w, err1 := s.db.GetUserByID(*rec.WhiteUserID)
	b, err2 := s.db.GetUserByID(*rec.BlackUserID)
	if err1 != nil || err2 != nil {
		return
	}

	// 3. Compute new ratings
	wRes := rating.Result(0.5)
	bRes := rating.Result(0.5)
	if rec.Result == "1-0" {
		wRes, bRes = 1.0, 0.0
	} else if rec.Result == "0-1" {
		wRes, bRes = 0.0, 1.0
	}

	wNew := rating.Update(rating.Player{
		Rating:     float64(w.Rating),
		RD:         float64(w.RD),
		Volatility: float64(w.Volatility),
	}, []rating.Opponent{{
		P: rating.Player{
			Rating:     float64(b.Rating),
			RD:         float64(b.RD),
			Volatility: float64(b.Volatility),
		},
		Score: wRes,
	}})

	bNew := rating.Update(rating.Player{
		Rating:     float64(b.Rating),
		RD:         float64(b.RD),
		Volatility: float64(b.Volatility),
	}, []rating.Opponent{{
		P: rating.Player{
			Rating:     float64(w.Rating),
			RD:         float64(w.RD),
			Volatility: float64(w.Volatility),
		},
		Score: bRes,
	}})

	// 4. Update DB
	s.db.UpdateUserRating(db.RatingUpdate{
		UserID:     w.ID,
		Rating:     float32(wNew.Rating),
		RD:         float32(wNew.RD),
		Volatility: float32(wNew.Volatility),
		Wins:       boolToInt(rec.Result == "1-0"),
		Losses:     boolToInt(rec.Result == "0-1"),
		Draws:      boolToInt(rec.Result == "1/2-1/2"),
	})

	s.db.UpdateUserRating(db.RatingUpdate{
		UserID:     b.ID,
		Rating:     float32(bNew.Rating),
		RD:         float32(bNew.RD),
		Volatility: float32(bNew.Volatility),
		Wins:       boolToInt(rec.Result == "0-1"),
		Losses:     boolToInt(rec.Result == "1-0"),
		Draws:      boolToInt(rec.Result == "1/2-1/2"),
	})

	slog.Info("ratings updated", "game_id", evt.GameID, "white_elo", wNew.Rating, "black_elo", bNew.Rating)
}

func boolToInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
