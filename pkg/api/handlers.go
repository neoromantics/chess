package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/bus"
	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/game"
	"github.com/neoromantics/chess/pkg/uci"
)

func (s *Server) isThinking(ctx context.Context, gameID string) bool {
	val, err := s.bus.GetState(ctx, "thinking:"+gameID)
	return err == nil && val == "1"
}

func (s *Server) setThinking(ctx context.Context, gameID string, val bool) {
	if val {
		s.bus.SetState(ctx, "thinking:"+gameID, "1", 2*time.Minute)
	} else {
		s.bus.DelState(ctx, "thinking:"+gameID)
	}
}

// scheduleEngineTimeout is the API-side safety net for engine requests
// whose worker died mid-search. Without it, the "thinking" flag persists
// for its full 2-minute TTL and the user is stuck — UX bug we shipped
// once and don't want to re-ship. After a generous budget (2× movetime
// + 3s) we clear the flag and broadcast fresh state so clients unblock.
//
// This is intentionally a Phase 1 placeholder; the production answer is
// Redis Streams + XCLAIM (Phase 5 hardening). The placeholder is good
// enough: a worker crash takes one extra move-time to recover, not the
// 2-minute eternity the original code shipped.
func (s *Server) scheduleEngineTimeout(gameID string, movetime time.Duration) {
	budget := 2*movetime + 3*time.Second
	if budget < 5*time.Second {
		budget = 5 * time.Second
	}
	time.AfterFunc(budget, func() {
		if !s.isThinking(context.Background(), gameID) {
			return // Worker responded; nothing to do.
		}
		slog.Warn("engine response timeout, clearing thinking flag", "game_id", gameID, "movetime", movetime)
		s.setThinking(context.Background(), gameID, false)
		s.broadcastEngineAbort(gameID)

		// Re-snapshot from PG and broadcast to unstick the UI.
		s.executeWithGameLock(context.Background(), gameID, func(entry *gameEntry) {
			entry.mu.Lock()
			snapshot := s.snapshotLocked(entry, s.getClock(context.Background(), entry.id))
			entry.mu.Unlock()
			s.hub.BroadcastState(gameID, snapshot)
		})
	})
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)

	// Auth — no requireAuth wrapper (these are how you become authed).
	s.mux.HandleFunc("POST /api/auth/signup", s.handleSignup)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)

	// User self-service — requires login.
	s.mux.HandleFunc("GET /api/user/me", requireAuth(s.handleMe))
	s.mux.HandleFunc("GET /api/user/profile", requireAuth(s.handleGetProfile))
	s.mux.HandleFunc("PUT /api/user/profile", requireAuth(s.handleUpdateProfile))
	s.mux.HandleFunc("POST /api/user/password", requireAuth(s.handleChangePassword))
	s.mux.HandleFunc("GET /api/user/stats", requireAuth(s.handleUserStats))
	s.mux.HandleFunc("GET /api/users/search", requireAuth(s.handleUserSearch))

	// Matchmaking — Phase 3. Redis sorted-set queue + leader-elected
	// pairing loop.
	s.mux.HandleFunc("POST /api/matchmaking/join", requireAuth(s.handleMatchmakingJoin))
	s.mux.HandleFunc("POST /api/matchmaking/leave", requireAuth(s.handleMatchmakingLeave))

	// Invites — Phase 2. Durable PG row drives /api/invites/pending for
	// reconnect sync; Redis user.evt.{id} drives live push.
	s.mux.HandleFunc("POST /api/invites/send", requireAuth(s.handleSendInvite))
	s.mux.HandleFunc("GET /api/invites/pending", requireAuth(s.handleListPendingInvites))
	s.mux.HandleFunc("POST /api/invites/{id}/accept", requireAuth(s.handleAcceptInvite))
	s.mux.HandleFunc("POST /api/invites/{id}/decline", requireAuth(s.handleDeclineInvite))
	s.mux.HandleFunc("POST /api/invites/{id}/cancel", requireAuth(s.handleCancelInvite))

	// Game management — requires login. Per-game endpoints below also
	// flow through getGame(), which double-checks ownership.
	s.mux.HandleFunc("POST /api/games/new", requireAuth(s.handleCreateGame))
	s.mux.HandleFunc("GET /api/games", requireAuth(s.handleListGames))
	s.mux.HandleFunc("DELETE /api/games/delete", requireAuth(s.handleDeleteGame))
	s.mux.HandleFunc("GET /api/state", requireAuth(s.handleState))
	s.mux.HandleFunc("POST /api/move", requireAuth(s.handleMove))
	s.mux.HandleFunc("POST /api/new", requireAuth(s.handleNew))
	s.mux.HandleFunc("POST /api/games/{id}/resign", requireAuth(s.handleResign))
	s.mux.HandleFunc("POST /api/games/{id}/offer-draw", requireAuth(s.handleOfferDraw))
	s.mux.HandleFunc("POST /api/games/{id}/accept-draw", requireAuth(s.handleAcceptDraw))
	s.mux.HandleFunc("POST /api/games/{id}/decline-draw", requireAuth(s.handleDeclineDraw))
	s.mux.HandleFunc("POST /api/games/{id}/offer-takeback", requireAuth(s.handleOfferTakeback))
	s.mux.HandleFunc("POST /api/games/{id}/accept-takeback", requireAuth(s.handleAcceptTakeback))
	s.mux.HandleFunc("POST /api/games/{id}/decline-takeback", requireAuth(s.handleDeclineTakeback))
	s.mux.HandleFunc("POST /api/hint", requireAuth(s.handleHint))
	s.mux.HandleFunc("POST /api/assess", requireAuth(s.handleAssess))
	s.mux.HandleFunc("POST /api/set_players", requireAuth(s.handleSetPlayers))
	s.mux.HandleFunc("POST /api/touch", requireAuth(s.handleTouch))
	s.mux.HandleFunc("POST /api/touch_move", requireAuth(s.handleTouchMove))
	s.mux.HandleFunc("POST /api/undo", requireAuth(s.handleUndo))
	s.mux.HandleFunc("GET /api/save", requireAuth(s.handleSave))
	s.mux.HandleFunc("POST /api/load", requireAuth(s.handleLoad))
	s.mux.HandleFunc("GET /api/replay.html", requireAuth(s.handleReplay))

	// WS opens are auth-gated too; otherwise an anonymous browser could
	// subscribe to a game's event stream. /ws/game/{game_id} carries game
	// events; /ws/user carries per-user events (invites, match-found).
	// /ws is kept as an alias for /ws/game so existing frontend clients
	// don't break during the Phase-1 rollout.
	s.mux.HandleFunc("GET /ws", requireAuth(s.handleWSGame))
	s.mux.HandleFunc("GET /ws/game", requireAuth(s.handleWSGame))
	s.mux.HandleFunc("GET /ws/user", requireAuth(s.handleWSUser))

	s.mux.HandleFunc("POST /api/ping", s.handlePing)
	s.mux.Handle("GET /assets/", http.FileServer(assetsFS))
	// Catch-all route for SPA navigation (Dashboard, GameView, etc.)
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /{path...}", s.handleIndex)
}

// gameEntry is a transient wrapper for a game during a single HTTP request.
type gameEntry struct {
	mu         sync.Mutex // Kept for API compatibility; local to the request
	game       *game.Game
	stopSearch atomic.Bool
	eventFired atomic.Bool
	id         string
	userID     int64
	createdAt  time.Time

	whiteThinkTime time.Duration
	blackThinkTime time.Duration

	// Platform fields (Phase 3)
	WhiteUserID *int64
	BlackUserID *int64
	TimeControl string
	Rated       bool
	Result      string
}

func (s *Server) getGame(r *http.Request) (*gameEntry, error) {
	id := r.URL.Query().Get("game_id")
	if id == "" {
		parts := strings.Split(r.URL.Path, "/")
		for i, p := range parts {
			if p == "game" && i+1 < len(parts) {
				id = parts[i+1]
				break
			}
		}
	}
	if id == "" {
		return nil, fmt.Errorf("missing game_id")
	}

	record, err := s.db.GetGame(id)
	if err != nil {
		return nil, fmt.Errorf("game not found")
	}

	user, ok := auth.GetUser(r.Context())
	// Games are strictly user-owned. The handler-side requireAuth wrapper
	// keeps anonymous callers out of game routes; this is a defence in
	// depth so direct calls into getGame can't accidentally leak state.
	if !ok || record.UserID != user.UserID {
		return nil, fmt.Errorf("game not found")
	}

	gameInst := game.NewGame()
	var history, historySAN []string
	json.Unmarshal([]byte(record.History), &history)
	json.Unmarshal([]byte(record.HistorySAN), &historySAN)
	gameInst.Load(record.FEN, history, record.EngineWhite, record.EngineBlack)
	gameInst.HistorySAN = historySAN

	entry := &gameEntry{
		game:           gameInst,
		id:             id,
		userID:         record.UserID,
		createdAt:      record.CreatedAt,
		whiteThinkTime: time.Duration(record.WhiteThinkTime) * time.Millisecond,
		blackThinkTime: time.Duration(record.BlackThinkTime) * time.Millisecond,
		// Platform fields
		WhiteUserID: record.WhiteUserID,
		BlackUserID: record.BlackUserID,
		TimeControl: record.TimeControl,
		Rated:       record.Rated,
		Result:      record.Result,
	}
	entry.stopSearch.Store(false)
	return entry, nil
}

func (s *Server) withGameLock(w http.ResponseWriter, r *http.Request, fn func(entry *gameEntry)) {
	// 1. Acquire Redis distributed lock first to prevent concurrent edits
	id := r.URL.Query().Get("game_id")
	if id == "" {
		http.Error(w, "missing game_id", 400)
		return
	}

	lock, err := s.bus.LockGame(r.Context(), id, 10*time.Second)
	if err != nil || lock == nil {
		http.Error(w, "game is currently locked by another process", http.StatusConflict)
		return
	}
	defer lock.Release(context.Background())

	// 2. Fetch fresh state from DB
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}

	// 3. Execute handler logic (which mutates entry.game)
	fn(entry)
}

func (s *Server) executeWithGameLock(ctx context.Context, gameID string, fn func(entry *gameEntry)) {
	lock, err := s.bus.LockGame(ctx, gameID, 5*time.Second)
	if err != nil || lock == nil {
		return
	}
	defer lock.Release(context.Background())

	record, err := s.db.GetGame(gameID)
	if err != nil {
		return
	}

	gameInst := game.NewGame()
	var history, historySAN []string
	json.Unmarshal([]byte(record.History), &history)
	json.Unmarshal([]byte(record.HistorySAN), &historySAN)
	gameInst.Load(record.FEN, history, record.EngineWhite, record.EngineBlack)
	gameInst.HistorySAN = historySAN

	entry := &gameEntry{
		game:           gameInst,
		id:             gameID,
		userID:         record.UserID,
		createdAt:      record.CreatedAt,
		whiteThinkTime: time.Duration(record.WhiteThinkTime) * time.Millisecond,
		blackThinkTime: time.Duration(record.BlackThinkTime) * time.Millisecond,
		// Platform fields
		WhiteUserID: record.WhiteUserID,
		BlackUserID: record.BlackUserID,
		TimeControl: record.TimeControl,
		Rated:       record.Rated,
		Result:      record.Result,
	}
	entry.stopSearch.Store(false)

	fn(entry)
}

func (s *Server) getClock(ctx context.Context, gameID string) *bus.GameClock {
	clock, err := s.bus.GetClock(ctx, gameID)
	if err != nil {
		// Default 10 minutes if not found
		return &bus.GameClock{
			WhiteMS:       600000,
			BlackMS:       600000,
			TurnStartedAt: time.Now().UnixMilli(),
		}
	}
	return clock
}

func (s *Server) syncGameToDB(entry *gameEntry, newAssess any) {
	entry.mu.Lock()
	gm := entry.game
	hist, _ := json.Marshal(gm.History)
	histSAN, _ := json.Marshal(gm.HistorySAN)
	status := gm.Status()

	// Load current assessments from DB if we're adding a new one
	var assessments []any
	record, err := s.db.GetGame(entry.id)
	if err == nil && record.Assessments != "" {
		json.Unmarshal([]byte(record.Assessments), &assessments)
	}
	if newAssess != nil {
		assessments = append(assessments, newAssess)
	}
	assessJSON, _ := json.Marshal(assessments)

	// Automatically set result if game ended
	res := entry.Result
	if status != game.StatusOngoing && res == "*" {
		switch status {
		case game.StatusCheckmate:
			if gm.Board.SideToMove == core.White {
				res = "0-1"
			} else {
				res = "1-0"
			}
		case game.StatusTimeout:
			if gm.WhiteTime <= 0 {
				res = "0-1"
			} else {
				res = "1-0"
			}
		case game.StatusStalemate, game.StatusDraw50, game.StatusDrawRepetition, game.StatusDrawInsufficient:
			res = "1/2-1/2"
		}
	}

	gameRec := &db.GameRecord{
		ID:             entry.id,
		UserID:         entry.userID,
		WhiteUserID:    entry.WhiteUserID,
		BlackUserID:    entry.BlackUserID,
		FEN:            gm.Board.FEN(),
		History:        string(hist),
		HistorySAN:     string(histSAN),
		EngineWhite:    gm.EngineWhite,
		EngineBlack:    gm.EngineBlack,
		WhiteThinkTime: int(entry.whiteThinkTime.Milliseconds()),
		BlackThinkTime: int(entry.blackThinkTime.Milliseconds()),
		TimeControl:    entry.TimeControl,
		Rated:          entry.Rated,
		Status:         string(status),
		Result:         res,
		Assessments:    string(assessJSON),
		CreatedAt:      entry.createdAt,
		UpdatedAt:      time.Now(),
	}
	clock := s.getClock(context.Background(), entry.id)
	snapshot := s.snapshotLocked(entry, clock)
	snapshot.Assessments = assessments

	// Detect game end and publish event
	shouldEmit := status != game.StatusOngoing && !entry.eventFired.Load()
	if shouldEmit {
		entry.eventFired.Store(true)
	}
	entry.mu.Unlock()

	s.db.SaveGame(gameRec)
	s.hub.BroadcastState(entry.id, snapshot)

	s.bus.Publish(context.Background(), bus.GameUpdatedChannel, bus.GameUpdatedEvent{
		GameID: entry.id,
	})

	if shouldEmit {
		slog.Info("detecting game end, publishing event", "game_id", entry.id, "status", status)
		event := bus.GameFinishedEvent{
			GameID:      entry.id,
			Status:      string(status),
			FEN:         gameRec.FEN,
			EngineWhite: gameRec.EngineWhite,
			EngineBlack: gameRec.EngineBlack,
			UserID:      gameRec.UserID,
		}
		if err := s.bus.Publish(context.Background(), bus.GameFinishedEventChannel, event); err != nil {
			slog.Error("failed to publish game finished event", "error", err)
		}
	}
}

// Auth Handlers

func (s *Server) secureCookie(name, value string) *http.Cookie {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if os.Getenv("HTTPS_ENABLED") == "true" {
		c.Secure = true
	}
	return c
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 32 {
		http.Error(w, "username must be 3-32 characters", 400)
		return
	}
	if len(req.Password) < 6 {
		http.Error(w, "password must be at least 6 characters", 400)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	user, err := s.db.CreateUser(req.Username, hash)
	if err != nil {
		http.Error(w, "username taken", 409)
		return
	}
	token, _ := auth.GenerateToken(user.ID, user.Username)
	http.SetCookie(w, s.secureCookie("token", token))
	writeJSON(w, map[string]any{"user": user, "token": token})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	user, err := s.db.GetUserByUsername(req.Username)
	if err != nil || !auth.CheckPasswordHash(req.Password, user.PasswordHash) {
		http.Error(w, "invalid credentials", 401)
		return
	}
	// Bump last_login; non-fatal if it fails.
	if err := s.db.UpdateLastLogin(user.ID); err != nil {
		slog.Warn("update last_login failed", "user_id", user.ID, "error", err)
	}
	token, _ := auth.GenerateToken(user.ID, user.Username)
	http.SetCookie(w, s.secureCookie("token", token))
	writeJSON(w, map[string]any{"user": user, "token": token})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	c := s.secureCookie("token", "")
	c.MaxAge = -1
	http.SetCookie(w, c)
	w.WriteHeader(204)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	// Fetch full profile from DB
	dbUser, err := s.db.GetUserByID(user.UserID)
	if err != nil {
		writeJSON(w, user)
		return
	}
	writeJSON(w, dbUser)
}

func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	dbUser, err := s.db.GetUserByID(user.UserID)
	if err != nil {
		http.Error(w, "user not found", 404)
		return
	}
	writeJSON(w, dbUser)
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
		Bio         string `json:"bio"`
		AvatarURL   string `json:"avatar_url"`
		Country     string `json:"country"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(req.DisplayName) > 64 {
		http.Error(w, "display name too long", 400)
		return
	}
	if len(req.Bio) > 500 {
		http.Error(w, "bio too long", 400)
		return
	}
	if err := s.db.UpdateUserProfile(user.UserID, req.DisplayName, req.Bio, req.AvatarURL, req.Country); err != nil {
		http.Error(w, "failed to update profile", 500)
		return
	}
	dbUser, _ := s.db.GetUserByID(user.UserID)
	writeJSON(w, dbUser)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(req.NewPassword) < 6 {
		http.Error(w, "password must be at least 6 characters", 400)
		return
	}
	dbUser, err := s.db.GetUserByID(user.UserID)
	if err != nil {
		http.Error(w, "user not found", 404)
		return
	}
	if !auth.CheckPasswordHash(req.CurrentPassword, dbUser.PasswordHash) {
		http.Error(w, "current password is incorrect", 401)
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	if err := s.db.UpdatePassword(user.UserID, hash); err != nil {
		http.Error(w, "failed to update password", 500)
		return
	}
	slog.Info("password changed", "user_id", user.UserID)
	w.WriteHeader(204)
}

func (s *Server) handleUserStats(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	stats, err := s.db.GetUserStats(user.UserID)
	if err != nil {
		http.Error(w, "failed to fetch stats", 500)
		return
	}
	writeJSON(w, stats)
}

// Game Management Handlers

func (s *Server) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	// Rate limit game creation
	ip := clientIP(r)
	if !s.gameLimiter.Allow(ip) {
		http.Error(w, "rate limit exceeded for game creation", http.StatusTooManyRequests)
		return
	}

	user, _ := auth.GetUser(r.Context())

	// Optional per-move engine think-time, in milliseconds. If the
	// frontend supplies a positive value we honor it; otherwise we
	// fall back to a sensible default. This makes the UI selector the
	// source of truth instead of a hardcoded backend constant.
	var req struct {
		WhiteThinkTime int   `json:"white_think_time"`
		BlackThinkTime int   `json:"black_think_time"`
		EngineWhite    *bool `json:"engine_white,omitempty"`
		EngineBlack    *bool `json:"engine_black,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	const defaultThink = 3 * time.Second
	whiteThink := defaultThink
	if req.WhiteThinkTime > 0 {
		whiteThink = time.Duration(req.WhiteThinkTime) * time.Millisecond
	}
	blackThink := defaultThink
	if req.BlackThinkTime > 0 {
		blackThink = time.Duration(req.BlackThinkTime) * time.Millisecond
	}

	id := uuid.New().String()
	slog.Info("creating new game", "game_id", id, "user_id", user.UserID)
	gm := game.NewGame()
	// Default to White human vs Black engine; UI may override.
	gm.EngineWhite = false
	gm.EngineBlack = true
	if req.EngineWhite != nil {
		gm.EngineWhite = *req.EngineWhite
	}
	if req.EngineBlack != nil {
		gm.EngineBlack = *req.EngineBlack
	}
	entry := &gameEntry{
		game:           gm,
		id:             id,
		userID:         user.UserID,
		createdAt:      time.Now(),
		whiteThinkTime: whiteThink,
		blackThinkTime: blackThink,
		// Platform fields
		WhiteUserID: &user.UserID,
		BlackUserID: nil, // against engine
		TimeControl: "engine",
		Rated:       false,
		Result:      "*",
	}
	if gm.EngineWhite {
		entry.WhiteUserID = nil
	}
	if !gm.EngineBlack {
		entry.BlackUserID = &user.UserID
	}
	entry.stopSearch.Store(false)

	s.syncGameToDB(entry, nil)
	s.bus.SetClock(context.Background(), id, bus.GameClock{
		WhiteMS:       600000,
		BlackMS:       600000,
		TurnStartedAt: time.Now().UnixMilli(),
	})
	go s.maybeTriggerEngine(entry)
	writeJSON(w, map[string]string{"game_id": id})
}

func (s *Server) handleListGames(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())
	records, err := s.db.ListGames(user.UserID)
	if err != nil {
		slog.Error("list games error", "error", err)
		http.Error(w, err.Error(), 500)
		return
	}
	if records == nil {
		records = []db.GameRecord{}
	}
	writeJSON(w, records)
}

func (s *Server) handleDeleteGame(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("game_id")

	// getGame() is the authorization gate — it checks user_id OR session_id
	// against the record. If it succeeds, the caller owns the game.
	if _, err := s.getGame(r); err != nil {
		slog.Warn("delete game: unauthorized or missing", "game_id", id, "error", err)
		http.Error(w, err.Error(), 404)
		return
	}

	// Abort any in-flight engine search on this game so workers don't
	// keep burning CPU on a deleted record.
	s.broadcastEngineAbort(id)

	rows, err := s.db.DeleteGame(id)
	if err != nil {
		slog.Error("delete game: db error", "game_id", id, "error", err)
		http.Error(w, err.Error(), 500)
		return
	}
	if rows == 0 {
		slog.Warn("delete game: no rows affected (already deleted?)", "game_id", id)
		http.Error(w, "game not found", 404)
		return
	}
	slog.Info("game deleted", "game_id", id)
	w.WriteHeader(204)
}

// Play Handlers

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	writeJSON(w, s.snapshotLocked(entry, s.getClock(context.Background(), entry.id)))
}

func (s *Server) handleTouch(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		Square string `json:"square"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	sq, _ := core.ParseSquare(req.Square)
	entry.mu.Lock()
	entry.game.Touch(sq)
	snapshot := s.snapshotLocked(entry, s.getClock(context.Background(), entry.id))
	entry.mu.Unlock()
	writeJSON(w, snapshot)
}

func (s *Server) handleTouchMove(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	entry.mu.Lock()
	entry.game.TouchMove = req.Enabled
	snapshot := s.snapshotLocked(entry, s.getClock(context.Background(), entry.id))
	entry.mu.Unlock()
	writeJSON(w, snapshot)
}

func (s *Server) deductClock(ctx context.Context, gameID string, side core.Color) *bus.GameClock {
	clock := s.getClock(ctx, gameID)
	now := time.Now().UnixMilli()
	elapsed := now - clock.TurnStartedAt
	if elapsed < 0 {
		elapsed = 0
	}

	if side == core.White {
		clock.WhiteMS -= elapsed
	} else {
		clock.BlackMS -= elapsed
	}
	if clock.WhiteMS < 0 {
		clock.WhiteMS = 0
	}
	if clock.BlackMS < 0 {
		clock.BlackMS = 0
	}

	clock.TurnStartedAt = now
	s.bus.SetClock(ctx, gameID, *clock)
	return clock
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	s.withGameLock(w, r, func(entry *gameEntry) {
		var req struct {
			Move string `json:"move"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		entry.mu.Lock()
		if s.isThinking(r.Context(), entry.id) {
			entry.mu.Unlock()
			http.Error(w, "engine is thinking", 409)
			return
		}
		if entry.game.Status() != game.StatusOngoing {
			entry.mu.Unlock()
			http.Error(w, "game is already finished", 403)
			return
		}
		if entry.game.EngineToMove() {
			entry.mu.Unlock()
			http.Error(w, "it is the engine's turn", 403)
			return
		}
		if entry.game.TouchLost {
			entry.mu.Unlock()
			http.Error(w, "touch-move violation: game lost", 403)
			return
		}

		m, err := entry.game.Board.ParseUCIMove(req.Move)
		if err != nil {
			entry.mu.Unlock()
			http.Error(w, "invalid move format", 400)
			return
		}
		matched, ok := game.MatchMove(entry.game.Board.GenerateLegalMoves(), m)
		if !ok {
			entry.mu.Unlock()
			http.Error(w, "illegal move", 403)
			return
		}

		movingSide := entry.game.Board.SideToMove
		entry.game.PlayMove(matched)
		clock := s.deductClock(r.Context(), entry.id, movingSide)
		snapshot := s.snapshotLocked(entry, clock)
		entry.mu.Unlock()

		s.syncGameToDB(entry, nil)
		go s.maybeTriggerEngine(entry)
		writeJSON(w, snapshot)
	})
}

func (s *Server) broadcastEngineAbort(gameID string) {
	slog.Info("broadcasting engine abort", "game_id", gameID)
	abort := bus.EngineAbort{GameID: gameID}
	if err := s.bus.Publish(context.Background(), bus.EngineAbortChannel, abort); err != nil {
		slog.Error("failed to publish engine abort", "error", err)
	}
}

func (s *Server) handleNew(w http.ResponseWriter, r *http.Request) {
	s.withGameLock(w, r, func(entry *gameEntry) {
		var req struct {
			EngineWhite bool `json:"engine_white"`
			EngineBlack bool `json:"engine_black"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		entry.mu.Lock()
		// Signal any current search to stop
		entry.stopSearch.Store(true)
		s.broadcastEngineAbort(entry.id)

		entry.game.Reset()
		entry.game.EngineWhite, entry.game.EngineBlack = req.EngineWhite, req.EngineBlack

		// Reset platform fields
		entry.Result = "*"
		user, _ := auth.GetUser(r.Context())
		entry.WhiteUserID = &user.UserID
		entry.BlackUserID = nil
		if entry.game.EngineWhite {
			entry.WhiteUserID = nil
		}
		if !entry.game.EngineBlack {
			entry.BlackUserID = &user.UserID
		}

		snapshot := s.snapshotLocked(entry, nil)
		entry.mu.Unlock()

		s.syncGameToDB(entry, nil)
		s.bus.SetClock(context.Background(), entry.id, bus.GameClock{
			WhiteMS:       600000,
			BlackMS:       600000,
			TurnStartedAt: time.Now().UnixMilli(),
		})
		go s.maybeTriggerEngine(entry)
		writeJSON(w, snapshot)
	})
}

func (s *Server) handleSetPlayers(w http.ResponseWriter, r *http.Request) {
	s.withGameLock(w, r, func(entry *gameEntry) {
		var req struct {
			EngineWhite    bool `json:"engine_white"`
			EngineBlack    bool `json:"engine_black"`
			WhiteThinkTime int  `json:"white_think_time"`
			BlackThinkTime int  `json:"black_think_time"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		entry.mu.Lock()
		entry.game.EngineWhite, entry.game.EngineBlack = req.EngineWhite, req.EngineBlack
		if req.WhiteThinkTime > 0 {
			entry.whiteThinkTime = time.Duration(req.WhiteThinkTime) * time.Millisecond
		}
		if req.BlackThinkTime > 0 {
			entry.blackThinkTime = time.Duration(req.BlackThinkTime) * time.Millisecond
		}

		entry.stopSearch.Store(true)
		s.broadcastEngineAbort(entry.id)

		snapshot := s.snapshotLocked(entry, s.getClock(context.Background(), entry.id))
		entry.mu.Unlock()
		s.syncGameToDB(entry, nil)
		go s.maybeTriggerEngine(entry)
		writeJSON(w, snapshot)
	})
}

func (s *Server) handleUndo(w http.ResponseWriter, r *http.Request) {
	s.withGameLock(w, r, func(entry *gameEntry) {
		entry.mu.Lock()
		entry.stopSearch.Store(true)
		s.broadcastEngineAbort(entry.id)
		entry.game.Undo()
		snapshot := s.snapshotLocked(entry, s.getClock(context.Background(), entry.id))
		entry.mu.Unlock()
		s.syncGameToDB(entry, nil)
		go s.maybeTriggerEngine(entry)
		writeJSON(w, snapshot)
	})
}

func (s *Server) handleHint(w http.ResponseWriter, r *http.Request) {
	// Rate limit engine analysis requests
	ip := clientIP(r)
	if !s.engineLimiter.Allow(ip) {
		http.Error(w, "rate limit exceeded for engine analysis", http.StatusTooManyRequests)
		return
	}

	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		MoveTime int `json:"movetime"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	entry.mu.Lock()
	if s.isThinking(r.Context(), entry.id) {
		entry.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	board := *entry.game.Board
	hist := game.CopyHistory(entry.game.HistoryHash())
	moveTime := time.Duration(req.MoveTime) * time.Millisecond

	s.setThinking(context.Background(), entry.id, true)
	entry.stopSearch.Store(false)
	snapshot := s.snapshotLocked(entry, s.getClock(context.Background(), entry.id))
	entry.mu.Unlock()

	s.hub.BroadcastState(entry.id, snapshot)

	// Dispatch hint request to worker
	job := bus.EngineRequest{
		GameID:   entry.id,
		FEN:      board.FEN(),
		History:  hist,
		MoveTime: moveTime,
		Context:  "hint",
	}
	go func() {
		if err := s.bus.Enqueue(context.Background(), bus.EngineRequestChannel, job); err != nil {
			slog.Error("failed to dispatch hint request", "error", err)
			entry.mu.Lock()
			s.setThinking(context.Background(), entry.id, false)
			entry.mu.Unlock()
			s.syncGameToDB(entry, nil)
			return
		}
		s.scheduleEngineTimeout(entry.id, moveTime)
	}()

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleAssess(w http.ResponseWriter, r *http.Request) {
	// Rate limit engine analysis requests
	ip := clientIP(r)
	if !s.engineLimiter.Allow(ip) {
		http.Error(w, "rate limit exceeded for engine analysis", http.StatusTooManyRequests)
		return
	}

	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		MoveTime int  `json:"movetime"`
		Index    *int `json:"index"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	entry.mu.Lock()
	if s.isThinking(r.Context(), entry.id) {
		entry.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	idx := entry.game.LastHumanMoveIndex()
	if req.Index != nil {
		idx = *req.Index
	}
	if idx < 0 || idx >= len(entry.game.UndoStack) {
		entry.mu.Unlock()
		http.Error(w, "no move to assess", 400)
		return
	}
	before, _ := entry.game.BoardsAroundMove(idx)
	userMove, _ := entry.game.UndoStack[idx].Move, entry.game.PlayerAt(idx)
	moveTime := time.Duration(req.MoveTime) * time.Millisecond

	s.setThinking(context.Background(), entry.id, true)
	entry.stopSearch.Store(false)
	snapshot := s.snapshotLocked(entry, s.getClock(context.Background(), entry.id))
	entry.mu.Unlock()

	s.hub.BroadcastState(entry.id, snapshot)

	// Dispatch assessment request to worker
	job := bus.EngineRequest{
		GameID:   entry.id,
		FEN:      before.FEN(),
		History:  game.CopyHistory(entry.game.HistoryHash()), // Roughly correct
		MoveTime: moveTime,
		Context:  "assess",
		Metadata: map[string]string{
			"move":  userMove.String(),
			"index": fmt.Sprintf("%d", idx),
		},
	}

	go func() {
		if err := s.bus.Enqueue(context.Background(), bus.EngineRequestChannel, job); err != nil {
			slog.Error("failed to dispatch assess request", "error", err)
			entry.mu.Lock()
			s.setThinking(context.Background(), entry.id, false)
			entry.mu.Unlock()
			s.syncGameToDB(entry, nil)
			return
		}
		s.scheduleEngineTimeout(entry.id, moveTime)
	}()

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}

	// TODO(Monetization): Game Export is a potential premium feature.
	// user, ok := auth.GetUser(r.Context())
	// if ok && !user.IsPremium {
	//    http.Error(w, "Premium subscription required for game export", http.StatusPaymentRequired)
	//    return
	// }

	entry.mu.Lock()
	defer entry.mu.Unlock()

	// Load assessments for the export
	var assessments []any
	record, err := s.db.GetGame(entry.id)
	if err == nil && record.Assessments != "" {
		json.Unmarshal([]byte(record.Assessments), &assessments)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=chess-game.json")

	exportData := map[string]any{
		"game_id":      entry.id,
		"start_fen":    entry.game.StartFEN,
		"moves":        entry.game.History,
		"engine_white": entry.game.EngineWhite,
		"engine_black": entry.game.EngineBlack,
		"assessments":  assessments,
		"exported_at":  time.Now(),
	}

	json.NewEncoder(w).Encode(exportData)
}

func (s *Server) handleLoad(w http.ResponseWriter, r *http.Request) {
	s.withGameLock(w, r, func(entry *gameEntry) {
		var sg struct {
			StartFEN                 string
			Moves                    []string
			EngineWhite, EngineBlack bool
		}
		json.NewDecoder(r.Body).Decode(&sg)
		entry.mu.Lock()
		entry.stopSearch.Store(true)
		s.broadcastEngineAbort(entry.id)
		entry.game.Load(sg.StartFEN, sg.Moves, sg.EngineWhite, sg.EngineBlack)
		snapshot := s.snapshotLocked(entry, s.getClock(context.Background(), entry.id))
		entry.mu.Unlock()
		s.syncGameToDB(entry, nil)
		go s.maybeTriggerEngine(entry)
		writeJSON(w, snapshot)
	})
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	data, _ := json.Marshal(entry.game.ReplayData())
	html := bytes.Replace(replayHTML, []byte(replayPlaceholder), data, 1)
	w.Header().Set("Content-Type", "text/html")
	w.Write(html)
}

// Infrastructure Handlers

func (s *Server) handleResign(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}

	user, _ := auth.GetUser(r.Context())
	entry.mu.Lock()
	if entry.game.Status() != game.StatusOngoing {
		entry.mu.Unlock()
		http.Error(w, "game already finished", 400)
		return
	}

	// Determine result based on who resigned
	if entry.WhiteUserID != nil && *entry.WhiteUserID == user.UserID {
		entry.Result = "0-1"
	} else if entry.BlackUserID != nil && *entry.BlackUserID == user.UserID {
		entry.Result = "1-0"
	} else {
		// Legacy/Guest support: resign current turn
		if entry.game.Board.SideToMove == core.White {
			entry.Result = "0-1"
		} else {
			entry.Result = "1-0"
		}
	}

	snapshot := s.snapshotLocked(entry, s.getClock(r.Context(), entry.id))
	entry.mu.Unlock()

	s.syncGameToDB(entry, nil)
	writeJSON(w, snapshot)
}

func (s *Server) handleOfferDraw(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}

	user, _ := auth.GetUser(r.Context())
	entry.mu.Lock()
	if entry.game.Status() != game.StatusOngoing {
		entry.mu.Unlock()
		http.Error(w, "game already finished", 400)
		return
	}

	side := core.White
	if entry.BlackUserID != nil && *entry.BlackUserID == user.UserID {
		side = core.Black
	}

	// Store in Redis
	s.bus.SetDrawOffer(r.Context(), entry.id, side)
	entry.mu.Unlock()

	// Notify opponent via user channel
	opponentID := int64(0)
	if side == core.White && entry.BlackUserID != nil {
		opponentID = *entry.BlackUserID
	}
	if side == core.Black && entry.WhiteUserID != nil {
		opponentID = *entry.WhiteUserID
	}

	if opponentID != 0 {
		s.hub.PublishUser(r.Context(), opponentID, "draw_offered", map[string]any{"game_id": entry.id})
	}

	w.WriteHeader(204)
}

func (s *Server) handleAcceptDraw(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}

	user, _ := auth.GetUser(r.Context())
	offer, err := s.bus.GetDrawOffer(r.Context(), entry.id)
	if err != nil || offer == nil {
		http.Error(w, "no active draw offer", 400)
		return
	}

	// Verify it wasn't you who offered
	side := core.White
	if entry.BlackUserID != nil && *entry.BlackUserID == user.UserID {
		side = core.Black
	}
	if offer.OfferedBy == side {
		http.Error(w, "cannot accept your own offer", 400)
		return
	}

	entry.mu.Lock()
	entry.Result = "1/2-1/2"
	s.bus.DelDrawOffer(r.Context(), entry.id)
	snapshot := s.snapshotLocked(entry, s.getClock(r.Context(), entry.id))
	entry.mu.Unlock()

	s.syncGameToDB(entry, nil)
	writeJSON(w, snapshot)
}

func (s *Server) handleDeclineDraw(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}

	s.bus.DelDrawOffer(r.Context(), entry.id)
	w.WriteHeader(204)
}

func (s *Server) handleOfferTakeback(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}

	user, _ := auth.GetUser(r.Context())
	entry.mu.Lock()
	if entry.game.Status() != game.StatusOngoing {
		entry.mu.Unlock()
		http.Error(w, "game already finished", 400)
		return
	}

	side := core.White
	if entry.BlackUserID != nil && *entry.BlackUserID == user.UserID {
		side = core.Black
	}

	s.bus.SetTakebackOffer(r.Context(), entry.id, side)
	entry.mu.Unlock()

	// Notify opponent
	opponentID := int64(0)
	if side == core.White && entry.BlackUserID != nil {
		opponentID = *entry.BlackUserID
	}
	if side == core.Black && entry.WhiteUserID != nil {
		opponentID = *entry.WhiteUserID
	}
	if opponentID != 0 {
		s.hub.PublishUser(r.Context(), opponentID, "takeback_offered", map[string]any{"game_id": entry.id})
	}
	w.WriteHeader(204)
}

func (s *Server) handleAcceptTakeback(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}

	user, _ := auth.GetUser(r.Context())
	offer, err := s.bus.GetTakebackOffer(r.Context(), entry.id)
	if err != nil || offer == nil {
		http.Error(w, "no active takeback offer", 400)
		return
	}

	side := core.White
	if entry.BlackUserID != nil && *entry.BlackUserID == user.UserID {
		side = core.Black
	}
	if offer.OfferedBy == side {
		http.Error(w, "cannot accept your own offer", 400)
		return
	}

	entry.mu.Lock()
	entry.game.Undo()
	s.bus.DelTakebackOffer(r.Context(), entry.id)

	// Reset turn start time so current player isn't penalized for the takeback time
	clock := s.getClock(r.Context(), entry.id)
	clock.TurnStartedAt = time.Now().UnixMilli()
	s.bus.SetClock(r.Context(), entry.id, *clock)

	snapshot := s.snapshotLocked(entry, clock)
	entry.mu.Unlock()

	s.syncGameToDB(entry, nil)
	writeJSON(w, snapshot)
}

func (s *Server) handleDeclineTakeback(w http.ResponseWriter, r *http.Request) {
	entry, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.bus.DelTakebackOffer(r.Context(), entry.id)
	w.WriteHeader(204)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := s.CheckHealth()
	if health.Status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	writeJSON(w, health)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if (r.URL.Path != "/" && path.Ext(r.URL.Path) != "") || strings.HasPrefix(r.URL.Path, "/assets/") {
		slog.Warn("asset not found", "path", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	slog.Info("serving index.html", "path", r.URL.Path)
	w.Header().Set("Content-Type", "text/html")
	w.Write(indexHTML)
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(204)
}

// Authoritative Engine Logic

func (s *Server) listenToEngine() {
	s.bus.Subscribe(context.Background(), bus.EngineResponseChannel, func(payload []byte) {
		var resp bus.EngineResponse
		if err := json.Unmarshal(payload, &resp); err != nil {
			slog.Error("failed to unmarshal engine response", "error", err)
			return
		}

		s.executeWithGameLock(context.Background(), resp.GameID, func(entry *gameEntry) {
			entry.mu.Lock()

			// Handle Context
			if resp.Context == "move" {
				if entry.game.EngineToMove() && entry.game.Status() == game.StatusOngoing {
					m, err := entry.game.Board.ParseUCIMove(resp.BestMove)
					if err == nil {
						if matched, ok := game.MatchMove(entry.game.Board.GenerateLegalMoves(), m); ok {
							slog.Info("engine move received from worker", "game_id", resp.GameID, "move", matched.String())
							movingSide := entry.game.Board.SideToMove
							entry.game.PlayMove(matched)
							s.deductClock(context.Background(), entry.id, movingSide)
						}
					}
				}
				s.setThinking(context.Background(), entry.id, false)
				entry.mu.Unlock()
				go s.syncGameToDB(entry, nil)
				time.AfterFunc(100*time.Millisecond, func() {
					s.maybeTriggerEngine(entry)
				})
				return
			}

			if resp.Context == "hint" {
				m, _ := core.ParseUCISimple(resp.BestMove)
				hintPayload := map[string]any{
					"move":  resp.BestMove,
					"from":  core.SquareName(m.From),
					"to":    core.SquareName(m.To),
					"promo": string(core.PromoChar(m.Promo)),
					"score": uci.ScoreToUCI(resp.Score),
					"depth": resp.Depth,
				}
				s.hub.BroadcastEvent(resp.GameID, "hint", hintPayload)
				s.setThinking(context.Background(), entry.id, false)
				entry.mu.Unlock()
				s.syncGameToDB(entry, nil)
				return
			}

			if resp.Context == "assess" {
				bestScore, _ := strconv.Atoi(resp.Metadata["best_score"])
				userScore, _ := strconv.Atoi(resp.Metadata["user_score"])
				idx, _ := strconv.Atoi(resp.Metadata["index"])

				playedM, _ := core.ParseUCISimple(resp.Metadata["move"])
				bestM, _ := core.ParseUCISimple(resp.BestMove)

				cpLoss := bestScore - userScore

				assessPayload := map[string]any{
					"index":      idx,
					"label":      game.ClassifyAssessment(playedM, bestM, cpLoss, bestScore, userScore),
					"move":       resp.Metadata["move"],
					"best_move":  resp.BestMove,
					"user_score": uci.ScoreToUCI(userScore),
					"best_score": uci.ScoreToUCI(bestScore),
					"cp_loss":    cpLoss,
				}
				s.hub.BroadcastEvent(resp.GameID, "assess", assessPayload)
				s.setThinking(context.Background(), entry.id, false)
				entry.mu.Unlock()
				s.syncGameToDB(entry, assessPayload)
				return
			}

			s.setThinking(context.Background(), entry.id, false)
			entry.mu.Unlock()
		})
	})
}

func (s *Server) maybeTriggerEngine(entry *gameEntry) {
	entry.mu.Lock()
	if s.isThinking(context.Background(), entry.id) {
		entry.mu.Unlock()
		return
	}

	if !entry.game.EngineToMove() || entry.game.Status() != game.StatusOngoing {
		entry.mu.Unlock()
		return
	}

	moveTime := entry.whiteThinkTime
	if entry.game.Board.SideToMove == core.Black {
		moveTime = entry.blackThinkTime
	}

	board := *entry.game.Board
	hist := game.CopyHistory(entry.game.HistoryHash())

	s.setThinking(context.Background(), entry.id, true)
	entry.stopSearch.Store(false)

	clock := s.getClock(context.Background(), entry.id)
	snapshot := s.snapshotLocked(entry, clock)
	entry.mu.Unlock()

	s.hub.BroadcastState(entry.id, snapshot)

	// Dispatch request to worker pool
	req := bus.EngineRequest{
		GameID:   entry.id,
		FEN:      board.FEN(),
		History:  hist,
		MoveTime: moveTime,
		Context:  "move",
	}

	go func() {
		if _, err := s.bus.StreamAdd(context.Background(), bus.EngineRequestChannel, req); err != nil {
			slog.Error("failed to dispatch engine request", "error", err)
			entry.mu.Lock()
			s.setThinking(context.Background(), entry.id, false)
			entry.mu.Unlock()
			s.syncGameToDB(entry, nil)
		} else {
			slog.Info("engine request dispatched to worker stream", "game_id", entry.id, "movetime", moveTime)
			s.scheduleEngineTimeout(entry.id, moveTime)
		}
	}()
}

func (s *Server) snapshotLocked(entry *gameEntry, clock *bus.GameClock) stateJSON {
	game := entry.game
	var lm *moveJSON
	if game.LastMove != nil {
		lm = &moveJSON{From: core.SquareName(game.LastMove.From), To: core.SquareName(game.LastMove.To)}
	}
	legal := game.Board.GenerateLegalMoves()
	legalStrs := make([]string, len(legal))
	for i, m := range legal {
		legalStrs[i] = m.String()
	}
	turn := "w"
	if game.Board.SideToMove == core.Black {
		turn = "b"
	}
	history, historySAN := append([]string(nil), game.History...), append([]string(nil), game.HistorySAN...)
	if history == nil {
		history = []string{}
	}
	if historySAN == nil {
		historySAN = []string{}
	}

	var assessments []any
	record, err := s.db.GetGame(entry.id)
	if err == nil && record.Assessments != "" {
		json.Unmarshal([]byte(record.Assessments), &assessments)
	}
	if assessments == nil {
		assessments = []any{}
	}

	whiteTime, blackTime := int64(0), int64(0)
	if clock != nil {
		whiteTime, blackTime = clock.WhiteMS, clock.BlackMS
	}

	return stateJSON{
		FEN: game.Board.FEN(), Turn: turn, EngineWhite: game.EngineWhite, EngineBlack: game.EngineBlack,
		EngineToMove: game.EngineToMove(), Status: string(game.Status()), InCheck: game.Board.InCheck(game.Board.SideToMove),
		LegalMoves: legalStrs, History: history, HistorySAN: historySAN, LastMove: lm,
		Thinking:  s.isThinking(context.Background(), entry.id),
		TouchMove: game.TouchMove, TouchedSquare: core.SquareName(game.TouchedSq),
		WhiteThinkTime: int(entry.whiteThinkTime / time.Millisecond),
		BlackThinkTime: int(entry.blackThinkTime / time.Millisecond),
		WhiteTime:      whiteTime,
		BlackTime:      blackTime,
		Assessments:    assessments,
		WhiteUserID:    entry.WhiteUserID,
		BlackUserID:    entry.BlackUserID,
	}
}

type stateJSON struct {
	FEN            string    `json:"fen"`
	Turn           string    `json:"turn"`
	EngineWhite    bool      `json:"engine_white"`
	EngineBlack    bool      `json:"engine_black"`
	EngineToMove   bool      `json:"engine_to_move"`
	Status         string    `json:"status"`
	InCheck        bool      `json:"in_check"`
	LegalMoves     []string  `json:"legal_moves"`
	History        []string  `json:"history"`
	HistorySAN     []string  `json:"history_san"`
	LastMove       *moveJSON `json:"last_move"`
	Thinking       bool      `json:"thinking"`
	TouchMove      bool      `json:"touch_move"`
	TouchedSquare  string    `json:"touched_square"`
	WhiteThinkTime int       `json:"white_think_time"`
	BlackThinkTime int       `json:"black_think_time"`
	WhiteTime      int64     `json:"white_time"`
	BlackTime      int64     `json:"black_time"`
	Assessments    []any     `json:"assessments"`
	WhiteUserID    *int64    `json:"white_user_id"`
	BlackUserID    *int64    `json:"black_user_id"`
}

type moveJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
