package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
)

// inviteTTL is the window a recipient has to act before the sweeper
// expires the invite. 60s mirrors common chess platforms — long enough
// to react, short enough that the pending list doesn't accumulate.
const inviteTTL = 60 * time.Second

// inviteWire is what /api/invites/{...} returns. Stable wire-protocol
// shape consumed by frontend/src/stores/invites.ts; renaming a field
// is a breaking change.
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

// authedUserID extracts the caller's user_id from the query string.
// The gateway's injectAuthedUser middleware appends this after JWT
// validation; no service downstream of the gateway re-validates JWTs.
func authedUserID(r *http.Request) (int64, bool) {
	s := r.URL.Query().Get("user_id")
	if s == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// userOwnsGame returns true if userID is a participant of the game
// record. Used at every game-keyed endpoint so a signed-in user can't
// read or write someone else's games via guessable UUIDs.
func userOwnsGame(userID int64, rec *db.GameRecord) bool {
	if rec == nil || userID == 0 {
		return false
	}
	if rec.WhiteUserID != nil && *rec.WhiteUserID == userID {
		return true
	}
	if rec.BlackUserID != nil && *rec.BlackUserID == userID {
		return true
	}
	return false
}

func (s *GameService) wireInvite(inv *db.Invite) inviteWire {
	w := inviteWire{
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
		w.FromUsername = u.Username
	}
	if u, err := s.db.GetUserByID(inv.ToUserID); err == nil {
		w.ToUsername = u.Username
	}
	return w
}

// ===== SEND =====

func (s *GameService) handleSendInvite(w http.ResponseWriter, r *http.Request) {
	from, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
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

	var recipient *db.User
	var err error
	switch {
	case strings.TrimSpace(req.ToUsername) != "":
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
	if recipient.ID == from {
		http.Error(w, "cannot invite yourself", http.StatusBadRequest)
		return
	}

	id := uuid.New()
	inv, err := s.db.CreateInvite(id, from, recipient.ID, req.TimeControl, req.Rated, time.Now().Add(inviteTTL))
	if err != nil {
		slog.Error("create invite failed", "from", from, "to", recipient.ID, "error", err)
		http.Error(w, "failed to create invite", http.StatusInternalServerError)
		return
	}
	slog.Info("invite created", "id", id, "from", from, "to", recipient.ID, "tc", req.TimeControl)

	wire := s.wireInvite(inv)
	pubPayload, _ := json.Marshal(wire)
	// Recipient sees the durable PG row + a live push.
	s.bus.PublishUserEvent(r.Context(), recipient.ID, eventbus.Event{
		Type:    eventbus.EvtInviteCreated,
		Payload: pubPayload,
	})
	// Sender gets a confirmation echo for their own outgoing list.
	s.bus.PublishUserEvent(r.Context(), from, eventbus.Event{
		Type:    eventbus.EvtInviteSent,
		Payload: pubPayload,
	})
	writeJSON(w, wire)
}

// ===== ACCEPT =====

func (s *GameService) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	to, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
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
	if inv.ToUserID != to {
		http.Error(w, "not your invite", http.StatusForbidden)
		return
	}
	if inv.Status != "pending" {
		http.Error(w, "invite is no longer pending", http.StatusConflict)
		return
	}

	// Create the game FIRST. If the AcceptInvite UPDATE then fails
	// (race with expiry/decline), we delete the orphan game record.
	// The opposite ordering would risk marking accepted with no game.
	gameID, err := s.createPvPGameFromInvite(inv)
	if err != nil {
		slog.Error("create pvp game failed", "error", err)
		http.Error(w, "failed to create game", http.StatusInternalServerError)
		return
	}
	rows, err := s.db.AcceptInvite(inv.ID, to, gameID)
	if err != nil || rows == 0 {
		_, _ = s.db.DeleteGame(gameID)
		s.invalidateGameCache(r.Context(), gameID)
		if err != nil {
			slog.Error("accept invite failed", "id", id, "error", err)
		}
		http.Error(w, "invite is no longer pending", http.StatusConflict)
		return
	}

	updated, err := s.db.GetInvite(inv.ID)
	if err != nil || updated == nil {
		// Best effort: synthesize a wire from the in-memory inv with
		// the accepted state so the response isn't empty.
		inv.Status = "accepted"
		inv.GameID = &gameID
		updated = inv
	}
	wire := s.wireInvite(updated)
	pubPayload, _ := json.Marshal(wire)
	s.bus.PublishUserEvent(r.Context(), inv.FromUserID, eventbus.Event{Type: eventbus.EvtInviteAccepted, Payload: pubPayload})
	s.bus.PublishUserEvent(r.Context(), inv.ToUserID, eventbus.Event{Type: eventbus.EvtInviteAccepted, Payload: pubPayload})
	slog.Info("invite accepted", "id", id, "game_id", gameID, "white", inv.FromUserID, "black", inv.ToUserID)
	writeJSON(w, wire)
}

// createPvPGameFromInvite mirrors handleCreatePvPGame's body but
// runs synchronously inside the accept handler so we can return the
// game_id in the HTTP response (the SPA needs it to navigate).
func (s *GameService) createPvPGameFromInvite(inv *db.Invite) (string, error) {
	id := uuid.New().String()
	gm := game.NewGame()
	white := inv.FromUserID
	black := inv.ToUserID
	rec := &db.GameRecord{
		ID:             id,
		WhiteUserID:    &white,
		BlackUserID:    &black,
		FEN:            gm.Board.FEN(),
		History:        "[]",
		HistorySAN:     "[]",
		EngineWhite:    false,
		EngineBlack:    false,
		WhiteThinkTime: 1000,
		BlackThinkTime: 1000,
		TimeControl:    inv.TimeControl,
		Rated:          inv.Rated,
		Status:         "ongoing",
		Result:         "*",
		Assessments:    "[]",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	// Use the cached save so a brand-new PvP game is hot in Redis
	// before either client's first /api/state poll lands.
	if err := s.saveGameCached(context.Background(), rec); err != nil {
		return id, err
	}
	if err := initClock(context.Background(), s.bus.Rdb(), rec); err != nil {
		slog.Error("clock init failed", "game_id", id, "error", err)
	}
	return id, nil
}

// ===== DECLINE / CANCEL =====

func (s *GameService) handleDeclineInvite(w http.ResponseWriter, r *http.Request) {
	uid, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := parseInviteID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := s.db.DeclineInvite(id, uid)
	if err != nil {
		slog.Error("decline invite failed", "id", id, "error", err)
		http.Error(w, "failed to decline", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		http.Error(w, "invite not found or already resolved", http.StatusNotFound)
		return
	}
	if inv, err := s.db.GetInvite(id); err == nil && inv != nil {
		wire := s.wireInvite(inv)
		payload, _ := json.Marshal(wire)
		s.bus.PublishUserEvent(r.Context(), inv.FromUserID, eventbus.Event{Type: eventbus.EvtInviteDeclined, Payload: payload})
		s.bus.PublishUserEvent(r.Context(), inv.ToUserID, eventbus.Event{Type: eventbus.EvtInviteDeclined, Payload: payload})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *GameService) handleCancelInvite(w http.ResponseWriter, r *http.Request) {
	uid, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := parseInviteID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := s.db.CancelInvite(id, uid)
	if err != nil {
		slog.Error("cancel invite failed", "id", id, "error", err)
		http.Error(w, "failed to cancel", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		http.Error(w, "invite not found or already resolved", http.StatusNotFound)
		return
	}
	if inv, err := s.db.GetInvite(id); err == nil && inv != nil {
		wire := s.wireInvite(inv)
		payload, _ := json.Marshal(wire)
		s.bus.PublishUserEvent(r.Context(), inv.FromUserID, eventbus.Event{Type: eventbus.EvtInviteCancelled, Payload: payload})
		s.bus.PublishUserEvent(r.Context(), inv.ToUserID, eventbus.Event{Type: eventbus.EvtInviteCancelled, Payload: payload})
	}
	w.WriteHeader(http.StatusNoContent)
}

// ===== PENDING (reconnect backlog) =====

func (s *GameService) handleListPendingInvites(w http.ResponseWriter, r *http.Request) {
	uid, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	received, err := s.db.ListPendingInvitesForUser(uid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sent, err := s.db.ListPendingInvitesFromUser(uid)
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
		out.Received = append(out.Received, s.wireInvite(&received[i]))
	}
	for i := range sent {
		out.Sent = append(out.Sent, s.wireInvite(&sent[i]))
	}
	writeJSON(w, out)
}

// ===== SWEEPER =====

// runInviteSweeper periodically expires pending invites whose TTL has
// passed and broadcasts an InviteExpired event so connected clients
// can drop the row from their UI without polling.
//
// In a multi-pod game-service deployment every replica will run this
// loop. That's fine: ExpireStaleInvites is atomic via the single PG
// UPDATE ... RETURNING — only one pod sees any given row in its result
// set. The redundant ticks waste a no-op SQL roundtrip each, which is
// cheap. If that ever shows up in metrics, pull this into a Redis-
// elected singleton.
func (s *GameService) runInviteSweeper(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			expired, err := s.db.ExpireStaleInvites()
			if err != nil {
				slog.Warn("invite sweeper expire failed", "error", err)
				continue
			}
			if len(expired) == 0 {
				continue
			}
			slog.Info("invite sweeper expired invites", "count", len(expired))
			for i := range expired {
				inv := &expired[i]
				wire := s.wireInvite(inv)
				payload, _ := json.Marshal(wire)
				s.bus.PublishUserEvent(ctx, inv.FromUserID, eventbus.Event{Type: eventbus.EvtInviteExpired, Payload: payload})
				s.bus.PublishUserEvent(ctx, inv.ToUserID, eventbus.Event{Type: eventbus.EvtInviteExpired, Payload: payload})
			}
		}
	}
}

// ===== helpers =====

func validTimeControl(tc string) bool {
	// Single rapid time control while we land core multiplayer
	// correctness; bullet/blitz/correspondence come back later.
	// See ROADMAP.md and the chess-paused-features memory.
	return tc == "15+10"
}

func parseInviteID(r *http.Request) (uuid.UUID, error) {
	if v := r.PathValue("id"); v != "" {
		return uuid.Parse(v)
	}
	// Fallback for path patterns the gateway can't /{id}-parse (it
	// proxies the raw URL through so this branch shouldn't normally
	// fire, but it's free insurance).
	if v := r.URL.Query().Get("id"); v != "" {
		return uuid.Parse(v)
	}
	return uuid.Nil, errors.New("missing invite id")
}
