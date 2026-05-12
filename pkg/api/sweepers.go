package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/neoromantics/chess/pkg/bus"
	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/leader"
)

// startInviteSweeper runs the periodic ExpireStaleInvites job on exactly
// one api pod at a time, picked via Redis-singleton leader election.
// Without this, every pod would sweep — multiplying PG writes by N and
// publishing N duplicate Expired events per invite.
//
// Cadence is conservative (30s). Faster sweeps don't change correctness
// because GET /api/invites/pending already filters expires_at > NOW(); the
// only thing the sweeper affects is when "expired" rows actually flip
// status (for audit/history) and when WS-connected recipients see the
// expiry event.
func (s *Server) startInviteSweeper(ctx context.Context) {
	election := leader.NewElection(s.bus.Rdb(), "invite-sweeper",
		leader.WithLeaseTTL(30*time.Second),
		leader.WithRetry(5*time.Second),
	)

	go election.Run(ctx, func(leaderCtx context.Context) {
		slog.Info("invite sweeper assumed leadership")
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-leaderCtx.Done():
				return
			case <-t.C:
				s.sweepInvites()
			}
		}
	})
}

// startClockManager runs the periodic flag-fall detection on exactly
// one api pod. It scans the 'active_games' Redis set and checks if
// either player's authoritative clock has hit zero.
func (s *Server) startClockManager(ctx context.Context) {
	election := leader.NewElection(s.bus.Rdb(), "clock-manager",
		leader.WithLeaseTTL(10*time.Second),
		leader.WithRetry(2*time.Second),
	)

	go election.Run(ctx, func(leaderCtx context.Context) {
		slog.Info("clock manager assumed leadership")
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-leaderCtx.Done():
				return
			case <-ticker.C:
				s.sweepClocks(leaderCtx)
			}
		}
	})
}

func (s *Server) sweepClocks(ctx context.Context) {
	gameIDs, err := s.bus.GetActiveGames(ctx)
	if err != nil {
		return
	}

	now := time.Now().UnixMilli()
	for _, id := range gameIDs {
		clock, err := s.bus.GetClock(ctx, id)
		if err != nil {
			continue
		}

		if clock.WhiteMS <= 0 || clock.BlackMS <= 0 || (now-clock.TurnStartedAt) > 600000 {
			// Fast check passed, do authoritative check under lock
			s.executeWithGameLock(ctx, id, func(entry *gameEntry) {
				movingSide := entry.game.Board.SideToMove
				
				// Check grace period before deducting
				if s.bus.IsInGracePeriod(ctx, id, movingSide) {
					// Pause clock: update TurnStartedAt to now so deduction is 0
					c := s.getClock(ctx, id)
					c.TurnStartedAt = time.Now().UnixMilli()
					s.bus.SetClock(ctx, id, *c)
					return
				}

				c := s.getClock(ctx, id)
				e := time.Now().UnixMilli() - c.TurnStartedAt
				if e < 0 { e = 0 }

				if movingSide == core.White { c.WhiteMS -= e } else { c.BlackMS -= e }
				
				if c.WhiteMS <= 0 || c.BlackMS <= 0 {
					if c.WhiteMS < 0 { c.WhiteMS = 0 }
					if c.BlackMS < 0 { c.BlackMS = 0 }
					s.bus.SetClock(ctx, id, *c)
					s.syncGameToDB(entry, nil)
				}
			})
		}
	}
}

// sweepInvites flips pending → expired for invites past their TTL in a
// single UPDATE ... RETURNING round trip, then publishes per-invite
// Expired events on both participants' user channels so their UIs can
// remove the entry without polling.
func (s *Server) sweepInvites() {
	expired, err := s.db.ExpireStaleInvites()
	if err != nil {
		slog.Warn("invite sweeper expire failed", "error", err)
		return
	}
	if len(expired) == 0 {
		return
	}
	slog.Info("invite sweeper expired invites", "count", len(expired))
	for i := range expired {
		inv := &expired[i]
		wire := s.toInviteWire(inv)
		s.hub.PublishUser(context.Background(), inv.FromUserID, WSInviteExpired, wire)
		s.hub.PublishUser(context.Background(), inv.ToUserID, WSInviteExpired, wire)
	}
}
