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
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
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

func (gw *Gateway) handleAdminActions(w http.ResponseWriter, r *http.Request) {
	if _, ok := gw.adminOnly(w, r); !ok {
		return
	}
	// Cursor pagination: ?before=<rfc3339> fetches the page older than
	// that timestamp. First-page callers omit it; subsequent pages
	// pass the CreatedAt of the last returned row. Empty / unparsable
	// values fall through to the first-page query so a typo can't
	// blank the panel.
	var rows []db.AdminAction
	var err error
	if v := r.URL.Query().Get("before"); v != "" {
		if t, perr := time.Parse(time.RFC3339Nano, v); perr == nil {
			rows, err = gw.db.ListAdminActionsBefore(t)
		} else {
			rows, err = gw.db.ListAdminActions()
		}
	} else {
		rows, err = gw.db.ListAdminActions()
	}
	if err != nil {
		slog.Error("admin actions: list failed", "error", err)
		http.Error(w, "failed to list actions", http.StatusInternalServerError)
		return
	}
	writeJSONGW(w, rows)
}

// handleAdminLiveGames lists every ongoing game with its current WS
// subscriber count attached. The viewer count is sourced from Redis
// PUBSUB NUMSUB on the same `game.evt.{id}` channels the gateway hub
// subscribes to — counting all subscribers (players + spectators)
// because the per-game pub/sub layer doesn't distinguish them. The
// SPA labels it "viewers" which the operator can interpret.
func (gw *Gateway) handleAdminLiveGames(w http.ResponseWriter, r *http.Request) {
	if _, ok := gw.adminOnly(w, r); !ok {
		return
	}
	games, err := gw.db.ListActiveGamesAdmin()
	if err != nil {
		slog.Error("admin live games: list failed", "error", err)
		http.Error(w, "failed to list live games", http.StatusInternalServerError)
		return
	}
	if len(games) == 0 {
		writeJSONGW(w, games)
		return
	}
	channels := make([]string, len(games))
	for i, g := range games {
		channels[i] = "game.evt." + g.ID
	}
	// Single round-trip: PUBSUB NUMSUB accepts a vector of channels.
	// 500ms bound — same posture as the queue-depth lookup; if Redis
	// is slow we'd rather render the rest of the panel with viewer=0
	// than time out wholesale.
	pCtx, cancel := context.WithTimeout(r.Context(), adminQueueDepthTimeout)
	defer cancel()
	subs, err := gw.bus.Rdb().PubSubNumSub(pCtx, channels...).Result()
	if err != nil {
		slog.Warn("admin live games: NUMSUB failed", "error", err)
		// Don't fail the whole response — the games list is still
		// useful without the viewer column.
		writeJSONGW(w, games)
		return
	}
	for i, g := range games {
		games[i].ViewerCount = subs["game.evt."+g.ID]
	}
	writeJSONGW(w, games)
}

// handleAdminDeleteUser is the only destructive admin endpoint in the
// first cut. Refuses self-deletion (irrecoverable; the operator would
// 404 their own /admin route) and bot deletion (would orphan the
// matchmaker pool). Requires `confirm_username` in the body to match
// the target's username exactly — same typed-confirm pattern git uses
// for branch deletes. Writes the audit row BEFORE the cascade so even
// a crashed DELETE leaves a trail.
//
// FK cascade is defined in schema.sql:
//
//	games.{white,black}_user_id  → SET NULL  (history preserved,
//	                                           slot anonymized)
//	invites.{from,to}_user_id    → CASCADE   (pending invites removed)
//	admin_actions.actor_user_id  → SET NULL  (target user as actor is
//	                                           not a thing; included
//	                                           for symmetry only)
func (gw *Gateway) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := gw.adminOnly(w, r)
	if !ok {
		return
	}
	idStr := r.PathValue("id")
	targetID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || targetID <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if targetID == actor.ID {
		http.Error(w, "cannot delete your own admin account", http.StatusBadRequest)
		return
	}
	target, err := gw.db.GetUserByID(targetID)
	if err != nil || target == nil {
		http.NotFound(w, r)
		return
	}
	// Block bot deletion: the matchmaker's in-memory pool would still
	// point at the missing row and dispatch into a broken game.
	// Removing a bot is a TODO-tagged matchmaker concern, not an
	// admin one.
	if isBotUsername(target.Username) {
		http.Error(w, "bot accounts must be removed via the matchmaker code path", http.StatusBadRequest)
		return
	}
	var body struct {
		ConfirmUsername string `json:"confirm_username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.ConfirmUsername != target.Username {
		http.Error(w, "confirm_username does not match target", http.StatusBadRequest)
		return
	}

	// Write the audit row first; if the DELETE crashes we still know
	// the attempt happened. Detail carries enough breadcrumbs to
	// reconstruct the action without the target row.
	actorID := actor.ID
	if err := gw.db.InsertAdminAction(
		&actorID, actor.Username, "delete_user",
		&targetID, target.Username,
		"target_rating="+strconv.Itoa(int(target.Rating)),
	); err != nil {
		slog.Error("admin delete: audit write failed", "actor", actor.Username, "target", target.Username, "error", err)
		http.Error(w, "audit write failed", http.StatusInternalServerError)
		return
	}
	n, err := gw.db.DeleteUser(targetID)
	if err != nil {
		slog.Error("admin delete: db failed", "actor", actor.Username, "target", target.Username, "error", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	slog.Info("admin delete user",
		"actor", actor.Username, "target", target.Username,
		"target_id", targetID, "rows", n)
	w.WriteHeader(http.StatusNoContent)
}

// isBotUsername mirrors cmd/game/bots.go:botSeeds at the gateway
// boundary so we can reject bot-deletion without round-tripping. Kept
// as a small literal set rather than re-using IsBot from the DB row —
// the column is informational; the matchmaker tags by username pool
// membership and that's the source of truth.
// TODO(matchmaker-engine-fallback): delete with the bot pool.
func isBotUsername(u string) bool {
	switch u {
	case "QueenKnight42", "PawnStorm91", "RookRunner_J", "BishopBlitz",
		"Knightfall_88", "EnPassantE4", "CastleCrusher", "MattedKing",
		"ZugzwangZee", "FianchettoFan", "TacticalT", "PrincessOfPawns":
		return true
	}
	return false
}
