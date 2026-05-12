package api

import (
	"context"
	"log/slog"
	"time"

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
