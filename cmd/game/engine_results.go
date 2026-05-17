package main

// Engine result stream consumer.
//
// Receives EngineResponse messages on the engine:results stream and
// routes them based on resp.Context: "move" lands as a MakeMove
// Command on game:commands (the move pipeline picks it up there),
// "hint" publishes directly on the per-game channel for the SPA's
// hint UI, "assess" feeds the per-ply analyzer's neighbour-pairing
// classifier. Pulled out of main.go to keep that file boot-only.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/redis/go-redis/v9"
)

// listenToEngineResults consumes the engine:results stream and converts
// each engine search outcome into a MakeMove Command on game:commands.
// The stream + consumer group pattern (vs the old Pub/Sub) means a
// game-service restart no longer loses results — they sit in the
// stream's pending entries list until acked.
func (s *GameService) listenToEngineResults(ctx context.Context) {
	hostname, _ := os.Hostname()
	s.bus.Consume(ctx, eventbus.StreamEngineResults, "game-engine-results-group", hostname, s.processEngineResult)
}

func (s *GameService) processEngineResult(ctx context.Context, msg redis.XMessage) {
	data, ok := msg.Values["data"].(string)
	if !ok {
		return
	}
	var resp eventbus.EngineResponse
	if err := json.Unmarshal([]byte(data), &resp); err != nil {
		slog.Error("engine-result unmarshal failed", "error", err)
		return
	}
	if resp.Context == "assess" {
		s.applyAssessmentResult(ctx, resp)
		return
	}
	if resp.Context == "hint" {
		s.publishHint(ctx, resp)
		return
	}
	if resp.Context != "move" {
		return
	}

	// Temp games short-circuit the Command pipeline. They have no PG
	// row and a different store; applyTempEngineMove does its own
	// lock + save + publish.
	if resp.Metadata["temp"] == "1" {
		s.applyTempEngineMove(ctx, resp)
		return
	}

	// Translate the engine's chosen move into a MakeMove Command so
	// the same code path (with its per-game lock) applies it. Clearing
	// the thinking sentinel happens inside handleMakeMove after the
	// save succeeds, so the SPA's spinner falls only when the move is
	// actually on the board.
	cmdPayload, _ := json.Marshal(eventbus.MakeMoveCmd{Move: resp.BestMove})
	cmd := eventbus.Command{
		Type:    eventbus.CmdMakeMove,
		GameID:  resp.GameID,
		UserID:  0, // System/Engine user
		Payload: cmdPayload,
	}

	// Bot-fallback humanizer: real humans don't move in 600ms; an
	// instant reply is the single biggest "this is a bot" tell. Detect
	// bot match and defer the dispatch by 3 or 10 seconds picked at
	// random. The 15s thinking-sentinel TTL (set in triggerEngineForMove)
	// covers this window so the SPA spinner persists.
	// TODO(matchmaker-engine-fallback): remove with the bot pool.
	if rec, err := s.getGameCached(ctx, resp.GameID); err == nil && isBotMatch(rec) {
		delay := pickBotReactionDelay()
		slog.Info("bot reaction delay", "game_id", resp.GameID, "delay", delay)
		// Capture rec.UpdatedAt as a generation token so the delayed
		// dispatcher can drop the move if anything (resign, /api/new
		// rematch, opponent disconnect) rewrote the row mid-delay.
		// Without this guard a stale 10s-delayed bestmove would land
		// onto a reset board and play whatever happened to be legal.
		gen := rec.UpdatedAt
		go func(c eventbus.Command, d time.Duration, dispatchGen time.Time) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
			case <-ctx.Done():
				return
			}
			// Use a fresh background context so cancellation of the
			// originating consume callback doesn't kill in-flight
			// rematch dispatches.
			bg := context.Background()
			if cur, err := s.getGameCached(bg, c.GameID); err == nil {
				if cur.Status != "ongoing" || !cur.UpdatedAt.Equal(dispatchGen) {
					slog.Info("dropping stale bot move", "game_id", c.GameID,
						"reason", "row changed during reaction delay")
					_ = s.bus.Rdb().Del(bg, "game:thinking:"+c.GameID).Err()
					return
				}
			}
			if _, err := s.bus.SendCommand(bg, c); err != nil {
				slog.Error("delayed engine MakeMove failed", "game_id", c.GameID, "error", err)
				// Clear the thinking sentinel so the SPA's spinner falls
				// instead of sitting at "thinking" until the 15s bot TTL
				// expires. Same rationale as the regular dispatch path below.
				_ = s.bus.Rdb().Del(bg, "game:thinking:"+c.GameID).Err()
			}
		}(cmd, delay, gen)
		return
	}

	if _, err := s.bus.SendCommand(ctx, cmd); err != nil {
		slog.Error("dispatch engine MakeMove failed", "game_id", resp.GameID, "error", err)
		// Engine's reply is lost — without clearing the sentinel the SPA
		// would spin until the (2*moveTime + 2s) TTL expires, then sit on
		// engine_to_move=true with no spinner and no way forward. Clearing
		// here lets the next /api/state read trigger a fresh search via
		// the lazy retrigger in handleState.
		_ = s.bus.Rdb().Del(ctx, "game:thinking:"+resp.GameID).Err()
	}
}

// publishHint delivers the engine hint result to the requester.
// Routing is private: if the request carried Metadata["requester_id"]
// (set by handleHTTPHint with the authed user), we publish on the
// caller's user.evt.{id} channel — the opponent in a PvP game must
// not see the hint. Falls back to game.evt.{id} only for temp games
// (single-player) and any legacy path without a requester tag.
//
// Payload shape {move, from, to, score, depth, promo?} matches
// GameView.onHintReceived; keeping that synthesis here lets the
// engine-worker stay context-agnostic.
func (s *GameService) publishHint(ctx context.Context, resp eventbus.EngineResponse) {
	uci := resp.BestMove
	payload := map[string]any{
		"move":  uci,
		"score": resp.Score,
		"depth": resp.Depth,
	}
	if len(uci) >= 4 {
		payload["from"] = uci[0:2]
		payload["to"] = uci[2:4]
		if len(uci) >= 5 {
			payload["promo"] = string(uci[4])
		}
	}
	pb, _ := json.Marshal(payload)
	evt := eventbus.Event{
		Type:    "hint",
		GameID:  resp.GameID,
		Payload: pb,
	}

	if rid := resp.Metadata["requester_id"]; rid != "" {
		uid, err := strconv.ParseInt(rid, 10, 64)
		if err == nil && uid > 0 {
			if err := s.bus.PublishUserEvent(ctx, uid, evt); err != nil {
				slog.Error("publish hint to user failed", "user_id", uid, "game_id", resp.GameID, "error", err)
			}
			return
		}
	}

	data, _ := json.Marshal(evt)
	if err := s.bus.Rdb().Publish(ctx, "game.evt."+resp.GameID, data).Err(); err != nil {
		slog.Error("publish hint failed", "game_id", resp.GameID, "error", err)
	}
}
