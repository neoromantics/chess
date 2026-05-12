package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/game"
)

// Invite wire types. The Type field is a stable wire-protocol identifier;
// frontends switch on it. Adding a new event is additive; renaming an
// existing one is a breaking change.
const (
	WSInviteCreated   = "invite_created"   // recipient: someone challenged you
	WSInviteSent      = "invite_sent"      // sender: confirmation echo
	WSInviteAccepted  = "invite_accepted"  // both: invite matured into a game
	WSInviteDeclined  = "invite_declined"  // sender: recipient said no
	WSInviteCancelled = "invite_cancelled" // recipient: sender withdrew
	WSInviteExpired   = "invite_expired"   // both: TTL ran out
)

// inviteWire is the JSON-friendly view of a stored invite shipped over
// WebSocket. We hydrate FromUsername/ToUsername so the recipient doesn't
// need a second roundtrip just to render "X invited you".
type inviteWire struct {
	ID           string  `json:"id"`
	FromUserID   int64   `json:"from_user_id"`
	FromUsername string  `json:"from_username,omitempty"`
	ToUserID     int64   `json:"to_user_id"`
	ToUsername   string  `json:"to_username,omitempty"`
	TimeControl  string  `json:"time_control"`
	Rated        bool    `json:"rated"`
	Status       string  `json:"status"`
	GameID       *string `json:"game_id,omitempty"`
	CreatedAt    string  `json:"created_at"`
	ExpiresAt    string  `json:"expires_at"`
}

func (s *Server) toInviteWire(inv *db.Invite) inviteWire {
	wire := inviteWire{
		ID:          inv.ID.String(),
		FromUserID:  inv.FromUserID,
		ToUserID:    inv.ToUserID,
		TimeControl: inv.TimeControl,
		Rated:       inv.Rated,
		Status:      inv.Status,
		GameID:      inv.GameID,
		CreatedAt:   inv.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:   inv.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if u, err := s.db.GetUserByID(inv.FromUserID); err == nil {
		wire.FromUsername = u.Username
	}
	if u, err := s.db.GetUserByID(inv.ToUserID); err == nil {
		wire.ToUsername = u.Username
	}
	return wire
}

// validTimeControl is the v1 allowlist. Matches the frontend picker.
func validTimeControl(tc string) bool {
	switch tc {
	case "1+0", "2+1", "3+2", "5+0", "10+5", "15+10", "corr-1d":
		return true
	}
	return false
}

// parseThinkFromTC maps a time-control string to an engine "per-move"
// think-time millis used as a sane default if either side later flips
// to engine play. Real game-clock semantics arrive in Phase 4 — until
// then think-time is the only timer.
func parseThinkFromTC(tc string) int {
	switch tc {
	case "1+0":
		return 500
	case "2+1":
		return 1000
	case "3+2":
		return 2000
	case "5+0":
		return 3000
	case "10+5":
		return 5000
	case "15+10":
		return 8000
	case "corr-1d":
		return 10000
	}
	return 3000
}

// ===== SEND =====

func (s *Server) handleSendInvite(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())

	var req struct {
		ToUsername  string `json:"to_username"`
		ToUserID    int64  `json:"to_user_id"`
		TimeControl string `json:"time_control"`
		Rated       bool   `json:"rated"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !validTimeControl(req.TimeControl) {
		http.Error(w, "unsupported time_control", http.StatusBadRequest)
		return
	}

	// Resolve recipient by username (preferred) or numeric id. Username
	// lets the UI keep PII out of the wire — the user types a friend's
	// name, never an opaque id.
	var recipient *db.User
	var err error
	switch {
	case req.ToUsername != "":
		recipient, err = s.db.GetUserByUsername(strings.TrimSpace(req.ToUsername))
	case req.ToUserID > 0:
		recipient, err = s.db.GetUserByID(req.ToUserID)
	default:
		http.Error(w, "to_username or to_user_id required", http.StatusBadRequest)
		return
	}
	if err != nil || recipient == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if recipient.ID == user.UserID {
		http.Error(w, "cannot invite yourself", http.StatusBadRequest)
		return
	}

	// 60s invite TTL is the same window most chess platforms use; long
	// enough for the recipient to react, short enough that the queue
	// doesn't fill with stale entries.
	const inviteTTL = 60 * time.Second
	id := uuid.New()
	inv, err := s.db.CreateInvite(id, user.UserID, recipient.ID, req.TimeControl, req.Rated, time.Now().Add(inviteTTL))
	if err != nil {
		slog.Error("create invite failed", "from", user.UserID, "to", recipient.ID, "error", err)
		http.Error(w, "failed to create invite", http.StatusInternalServerError)
		return
	}
	slog.Info("invite created", "id", id, "from", user.UserID, "to", recipient.ID, "tc", req.TimeControl)

	wire := s.toInviteWire(inv)
	// Durable PG row is the source of truth (per the delivery contract).
	// Pub/sub is the acceleration layer: recipient sees it instantly if
	// online, learns about it from /api/invites/pending on reconnect.
	s.hub.PublishUser(context.Background(), recipient.ID, WSInviteCreated, wire)
	s.hub.PublishUser(context.Background(), user.UserID, WSInviteSent, wire)
	writeJSON(w, wire)
}

// ===== ACCEPT =====

func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())
	id, err := parseInviteID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	inv, err := s.db.GetInvite(id)
	if err != nil || inv == nil {
		http.Error(w, "invite not found", http.StatusNotFound)
		return
	}
	if inv.ToUserID != user.UserID {
		http.Error(w, "not your invite", http.StatusForbidden)
		return
	}
	if inv.Status != "pending" {
		http.Error(w, "invite is no longer pending", http.StatusConflict)
		return
	}

	// Create the game first; if the AcceptInvite UPDATE then fails (race
	// with expiry/decline), we delete the dangling game record. This
	// ordering avoids the inverse problem where the invite is marked
	// accepted but no game exists.
	gameID, err := s.createPvPGame(inv)
	if err != nil {
		slog.Error("create pvp game failed", "error", err)
		http.Error(w, "failed to create game", http.StatusInternalServerError)
		return
	}

	rows, err := s.db.AcceptInvite(inv.ID, user.UserID, gameID)
	if err != nil || rows == 0 {
		// Lost the race — clean up the orphan game.
		_, _ = s.db.DeleteGame(gameID)
		if err != nil {
			slog.Error("accept invite failed", "id", id, "error", err)
		}
		http.Error(w, "invite is no longer pending", http.StatusConflict)
		return
	}

	// Re-read so wire payload reflects accepted state + game_id.
	updated, err := s.db.GetInvite(inv.ID)
	if err != nil {
		updated = inv
		updated.Status = "accepted"
		updated.GameID = &gameID
	}
	wire := s.toInviteWire(updated)

	s.hub.PublishUser(context.Background(), inv.FromUserID, WSInviteAccepted, wire)
	s.hub.PublishUser(context.Background(), inv.ToUserID, WSInviteAccepted, wire)
	slog.Info("invite accepted", "id", id, "game_id", gameID, "white", inv.FromUserID, "black", inv.ToUserID)

	writeJSON(w, wire)
}

// createPvPGame writes the initial GameRecord for a fresh PvP match. The
// sender plays white, the recipient plays black — a convention shared
// with major chess platforms. Time-control + rated flag flow straight
// from the invite.
func (s *Server) createPvPGame(inv *db.Invite) (string, error) {
	id := uuid.New().String()
	gm := game.NewGame()
	gm.EngineWhite = false
	gm.EngineBlack = false

	histJSON, _ := json.Marshal(gm.History)
	sanJSON, _ := json.Marshal(gm.HistorySAN)
	white := inv.FromUserID
	black := inv.ToUserID
	think := parseThinkFromTC(inv.TimeControl)
	now := time.Now()
	rec := &db.GameRecord{
		ID:             id,
		UserID:         inv.FromUserID, // legacy column, retained until 000004
		WhiteUserID:    &white,
		BlackUserID:    &black,
		FEN:            gm.Board.FEN(),
		History:        string(histJSON),
		HistorySAN:     string(sanJSON),
		EngineWhite:    false,
		EngineBlack:    false,
		WhiteThinkTime: think,
		BlackThinkTime: think,
		TimeControl:    inv.TimeControl,
		Rated:          inv.Rated,
		Status:         string(game.StatusOngoing),
		Result:         "*",
		Assessments:    "[]",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.db.SaveGame(rec); err != nil {
		return "", err
	}
	return id, nil
}

// ===== DECLINE =====

func (s *Server) handleDeclineInvite(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())
	id, err := parseInviteID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := s.db.DeclineInvite(id, user.UserID)
	if err != nil {
		slog.Error("decline invite failed", "id", id, "error", err)
		http.Error(w, "failed to decline", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		http.Error(w, "invite not found or already resolved", http.StatusNotFound)
		return
	}
	inv, _ := s.db.GetInvite(id)
	if inv != nil {
		wire := s.toInviteWire(inv)
		s.hub.PublishUser(context.Background(), inv.FromUserID, WSInviteDeclined, wire)
		s.hub.PublishUser(context.Background(), inv.ToUserID, WSInviteDeclined, wire)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ===== CANCEL =====

func (s *Server) handleCancelInvite(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())
	id, err := parseInviteID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := s.db.CancelInvite(id, user.UserID)
	if err != nil {
		slog.Error("cancel invite failed", "id", id, "error", err)
		http.Error(w, "failed to cancel", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		http.Error(w, "invite not found or already resolved", http.StatusNotFound)
		return
	}
	inv, _ := s.db.GetInvite(id)
	if inv != nil {
		wire := s.toInviteWire(inv)
		s.hub.PublishUser(context.Background(), inv.FromUserID, WSInviteCancelled, wire)
		s.hub.PublishUser(context.Background(), inv.ToUserID, WSInviteCancelled, wire)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ===== PENDING (reconnect backlog) =====

func (s *Server) handleListPendingInvites(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())
	received, err := s.db.ListPendingInvitesForUser(user.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sent, err := s.db.ListPendingInvitesFromUser(user.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := struct {
		Received []inviteWire `json:"received"`
		Sent     []inviteWire `json:"sent"`
	}{
		Received: make([]inviteWire, 0, len(received)),
		Sent:     make([]inviteWire, 0, len(sent)),
	}
	for i := range received {
		out.Received = append(out.Received, s.toInviteWire(&received[i]))
	}
	for i := range sent {
		out.Sent = append(out.Sent, s.toInviteWire(&sent[i]))
	}
	writeJSON(w, out)
}

// ===== USER SEARCH (for invite autocomplete) =====

func (s *Server) handleUserSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		writeJSON(w, []db.UserSummary{})
		return
	}
	results, err := s.db.SearchUsersByPrefix(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Don't surface the calling user as their own search target.
	user, _ := auth.GetUser(r.Context())
	filtered := results[:0]
	for _, u := range results {
		if u.ID != user.UserID {
			filtered = append(filtered, u)
		}
	}
	writeJSON(w, filtered)
}

// ===== helpers =====

// parseInviteID accepts the id either from a path segment
// (/api/invites/{id}/...) or query string fallback.
func parseInviteID(r *http.Request) (uuid.UUID, error) {
	if v := r.PathValue("id"); v != "" {
		return uuid.Parse(v)
	}
	if v := r.URL.Query().Get("id"); v != "" {
		return uuid.Parse(v)
	}
	return uuid.Nil, errors.New("missing invite id")
}
