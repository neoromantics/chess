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

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /api/auth/signup", s.handleSignup)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/user/me", s.handleMe)
	s.mux.HandleFunc("GET /api/user/profile", s.handleGetProfile)
	s.mux.HandleFunc("PUT /api/user/profile", s.handleUpdateProfile)
	s.mux.HandleFunc("GET /api/user/stats", s.handleUserStats)
	s.mux.HandleFunc("POST /api/games/new", s.handleCreateGame)
	s.mux.HandleFunc("GET /api/games", s.handleListGames)
	s.mux.HandleFunc("DELETE /api/games/delete", s.handleDeleteGame)
	s.mux.HandleFunc("GET /api/state", s.handleState)
	s.mux.HandleFunc("POST /api/move", s.handleMove)
	s.mux.HandleFunc("POST /api/new", s.handleNew)
	s.mux.HandleFunc("POST /api/hint", s.handleHint)
	s.mux.HandleFunc("POST /api/assess", s.handleAssess)
	s.mux.HandleFunc("POST /api/set_players", s.handleSetPlayers)
	s.mux.HandleFunc("POST /api/touch", s.handleTouch)
	s.mux.HandleFunc("POST /api/touch_move", s.handleTouchMove)
	s.mux.HandleFunc("POST /api/undo", s.handleUndo)
	s.mux.HandleFunc("POST /api/ping", s.handlePing)
	s.mux.HandleFunc("GET /api/save", s.handleSave)
	s.mux.HandleFunc("POST /api/load", s.handleLoad)
	s.mux.HandleFunc("GET /api/replay.html", s.handleReplay)
	s.mux.HandleFunc("GET /ws", s.handleWS)
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
	sessionID  string
	createdAt  time.Time

	whiteThinkTime time.Duration
	blackThinkTime time.Duration
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

	user, _ := auth.GetUser(r.Context())
	sessionID := auth.GetSessionID(r.Context())

	authorized := false
	if (user != nil && record.UserID == user.UserID) || (record.UserID == 0 && record.SessionID == sessionID) {
		authorized = true
	}
	if !authorized {
		return nil, fmt.Errorf("unauthorized")
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
		sessionID:      record.SessionID,
		createdAt:      record.CreatedAt,
		whiteThinkTime: time.Duration(record.WhiteThinkTime) * time.Millisecond,
		blackThinkTime: time.Duration(record.BlackThinkTime) * time.Millisecond,
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
		sessionID:      record.SessionID,
		createdAt:      record.CreatedAt,
		whiteThinkTime: time.Duration(record.WhiteThinkTime) * time.Millisecond,
		blackThinkTime: time.Duration(record.BlackThinkTime) * time.Millisecond,
	}

	fn(entry)
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

	gameRec := &db.GameRecord{
		ID:             entry.id,
		UserID:         entry.userID,
		SessionID:      entry.sessionID,
		FEN:            gm.Board.FEN(),
		History:        string(hist),
		HistorySAN:     string(histSAN),
		EngineWhite:    gm.EngineWhite,
		EngineBlack:    gm.EngineBlack,
		WhiteThinkTime: int(entry.whiteThinkTime.Milliseconds()),
		BlackThinkTime: int(entry.blackThinkTime.Milliseconds()),
		Status:         string(status),
		Assessments:    string(assessJSON),
		CreatedAt:      entry.createdAt,
		UpdatedAt:      time.Now(),
	}
	snapshot := s.snapshotLocked(entry)
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
	sessionID := auth.GetSessionID(r.Context())
	var userID int64
	if user != nil {
		userID = user.UserID
	}

	id := uuid.New().String()
	slog.Info("creating new game", "game_id", id, "user_id", userID, "session_id", sessionID)
	gm := game.NewGame()
	gm.EngineBlack = true // Default setup: White human vs Black engine
	entry := &gameEntry{
		game:      gm,
		id:        id,
		userID:    userID,
		sessionID: sessionID,
		createdAt: time.Now(),
		// This is the engine's *per-move* search budget, not a game clock.
		// 10 minutes here meant every engine reply took 10 minutes to come
		// back — looked like "engine not responding." 3s is a reasonable
		// starting point; the UI can raise it via /api/set_players.
		whiteThinkTime: 3 * time.Second,
		blackThinkTime: 3 * time.Second,
	}
	entry.stopSearch.Store(false)

	s.syncGameToDB(entry, nil)
	writeJSON(w, map[string]string{"game_id": id})
}

func (s *Server) handleListGames(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())
	sessionID := auth.GetSessionID(r.Context())

	var userID int64
	if user != nil {
		userID = user.UserID
	}

	records, err := s.db.ListGames(userID, sessionID)
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
	writeJSON(w, s.snapshotLocked(entry))
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
	snapshot := s.snapshotLocked(entry)
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
	snapshot := s.snapshotLocked(entry)
	entry.mu.Unlock()
	writeJSON(w, snapshot)
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

		entry.game.PlayMove(matched)
		snapshot := s.snapshotLocked(entry)
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
		snapshot := s.snapshotLocked(entry)
		entry.mu.Unlock()

		s.syncGameToDB(entry, nil)
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

		snapshot := s.snapshotLocked(entry)
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
		snapshot := s.snapshotLocked(entry)
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
	snapshot := s.snapshotLocked(entry)
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
		}
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
	snapshot := s.snapshotLocked(entry)
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
		}
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
		snapshot := s.snapshotLocked(entry)
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
							entry.game.PlayMove(matched)
						}
					}
				}
				s.setThinking(context.Background(), entry.id, false)
				entry.mu.Unlock()
				s.syncGameToDB(entry, nil)
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

	snapshot := s.snapshotLocked(entry)
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
		if err := s.bus.Enqueue(context.Background(), bus.EngineRequestChannel, req); err != nil {
			slog.Error("failed to dispatch engine request", "error", err)
			entry.mu.Lock()
			s.setThinking(context.Background(), entry.id, false)
			entry.mu.Unlock()
			s.syncGameToDB(entry, nil)
		} else {
			slog.Info("engine request dispatched to worker", "game_id", entry.id, "movetime", moveTime)
		}
	}()
}

func (s *Server) snapshotLocked(entry *gameEntry) stateJSON {
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

	return stateJSON{
		FEN: game.Board.FEN(), Turn: turn, EngineWhite: game.EngineWhite, EngineBlack: game.EngineBlack,
		EngineToMove: game.EngineToMove(), Status: string(game.Status()), InCheck: game.Board.InCheck(game.Board.SideToMove),
		LegalMoves: legalStrs, History: history, HistorySAN: historySAN, LastMove: lm,
		Thinking:  s.isThinking(context.Background(), entry.id),
		TouchMove: game.TouchMove, TouchedSquare: core.SquareName(game.TouchedSq),
		WhiteThinkTime: int(entry.whiteThinkTime / time.Millisecond),
		BlackThinkTime: int(entry.blackThinkTime / time.Millisecond),
		Assessments:    assessments,
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
	Assessments    []any     `json:"assessments"`
}

type moveJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
