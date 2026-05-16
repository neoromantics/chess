package main

// Admin dashboard endpoints. Read-only first pass — analytics + recent
// signups, no write surface. Power tools (delete-user, ban, etc.)
// land in a follow-up after we've used this view in production for a
// stretch and know what's actually useful.
//
// Auth model:
//   * Gateway's existing JWT middleware populates auth.GetUser.
//   * adminOnly inside each handler re-fetches the user row and gates
//     on User.IsAdmin. The is_admin flag is FALSE by default; flip it
//     by SQL ("UPDATE users SET is_admin = TRUE WHERE username = '…';"
//     run via `kubectl exec deploy/chess-db -- psql …`). The signup
//     path never sets it, so a fresh account can't escalate.
//   * Non-admins get 404 (not 403) so the existence of /admin doesn't
//     leak — same existence-hiding convention used by userOwnsGame.
//
// Queue depth: read directly from Redis (mm:queue:{tc}) since the
// gateway already holds a bus client and the data isn't worth a round
// trip through game-service. Stays in sync because the gateway uses
// the same key naming convention the matchmaker writes.

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/db"
)

// adminSupportedTCs mirrors cmd/game/matchmaker.go:supportedTCs. Kept
// local so the gateway doesn't take a hard dep on cmd/game internals
// for the sake of one constant; if a third TC lands, update both.
var adminSupportedTCs = []string{"3+0", "10+0"}

// adminQueueDepthTimeout caps the Redis poll for the per-TC queue
// depths. The metric is best-effort: if Redis is slow, the dashboard
// should still show user / signup counts rather than time out wholesale.
const adminQueueDepthTimeout = 500 * time.Millisecond

// adminOnly returns the caller iff they're authenticated AND
// is_admin=true in the DB. Writes the 404 response itself on failure;
// handlers stop on a single line. Re-fetches the user row instead of
// trusting the JWT so a freshly-revoked admin can't keep operating
// off a long-lived cookie.
func (gw *Gateway) adminOnly(w http.ResponseWriter, r *http.Request) (*db.User, bool) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.NotFound(w, r)
		return nil, false
	}
	dbUser, err := gw.db.GetUserByID(user.UserID)
	if err != nil || dbUser == nil || !dbUser.IsAdmin {
		http.NotFound(w, r)
		return nil, false
	}
	return dbUser, true
}

func (gw *Gateway) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	if _, ok := gw.adminOnly(w, r); !ok {
		return
	}
	users, err := gw.db.CountUsers()
	if err != nil {
		slog.Error("admin overview: count users failed", "error", err)
		http.Error(w, "failed to load overview", http.StatusInternalServerError)
		return
	}
	day, week, err := gw.db.CountRecentSignups()
	if err != nil {
		slog.Error("admin overview: signups count failed", "error", err)
		http.Error(w, "failed to load overview", http.StatusInternalServerError)
		return
	}
	active, err := gw.db.CountActiveGames()
	if err != nil {
		slog.Error("admin overview: active games count failed", "error", err)
		http.Error(w, "failed to load overview", http.StatusInternalServerError)
		return
	}

	// Per-TC queue depth via direct Redis ZCard. Best-effort: each
	// failure logs and slot stays at 0 so a partial dashboard renders.
	depth := make(map[string]int64, len(adminSupportedTCs))
	qCtx, cancel := context.WithTimeout(r.Context(), adminQueueDepthTimeout)
	defer cancel()
	for _, tc := range adminSupportedTCs {
		n, err := gw.bus.Rdb().ZCard(qCtx, "mm:queue:"+tc).Result()
		if err != nil {
			slog.Warn("admin overview: queue depth read failed", "tc", tc, "error", err)
			depth[tc] = 0
			continue
		}
		depth[tc] = n
	}

	out := db.AdminOverview{
		Users:         users,
		SignupsDay:    day,
		SignupsWeek:   week,
		ActiveGames:   active,
		QueueDepthMap: depth,
	}
	writeJSONGW(w, out)
}

func (gw *Gateway) handleAdminSignups(w http.ResponseWriter, r *http.Request) {
	if _, ok := gw.adminOnly(w, r); !ok {
		return
	}
	rows, err := gw.db.ListRecentSignups()
	if err != nil {
		slog.Error("admin signups: list failed", "error", err)
		http.Error(w, "failed to list signups", http.StatusInternalServerError)
		return
	}
	// Keep the wire response a plain JSON array so the SPA can render
	// it without unwrapping; the recent-signups panel cares about
	// nothing more than this list.
	writeJSONGW(w, rows)
}
