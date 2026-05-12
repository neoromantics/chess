package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
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

func (s *Server) getGame(r *http.Request) (*gameEntry, string, error) {
	id := r.URL.Query().Get("game_id")
	if id == "" {
		return nil, "", fmt.Errorf("missing game_id")
	}

	s.mu.Lock()
	entry, ok := s.games[id]
	if ok {
		entry.lastUsed = time.Now()
		s.mu.Unlock()
		return entry, id, nil
	}
	s.mu.Unlock()

	record, err := s.db.GetGame(id)
	if err != nil {
		return nil, "", fmt.Errorf("game not found")
	}

	user, _ := auth.GetUser(r.Context())
	sessionID := auth.GetSessionID(r.Context())

	authorized := false
	if (user != nil && record.UserID == user.UserID) || (record.UserID == 0 && record.SessionID == sessionID) {
		authorized = true
	}

	if !authorized {
		return nil, "", fmt.Errorf("unauthorized")
	}

	gameInst := game.NewGame()
	var history, historySAN []string
	json.Unmarshal([]byte(record.History), &history)
	json.Unmarshal([]byte(record.HistorySAN), &historySAN)
	gameInst.Load(record.FEN, history, record.EngineWhite, record.EngineBlack)
	gameInst.HistorySAN = historySAN

	entry = &gameEntry{
		game:           gameInst,
		id:             id,
		userID:         record.UserID,
		sessionID:      record.SessionID,
		createdAt:      record.CreatedAt,
		lastUsed:       time.Now(),
		whiteThinkTime: 1000 * time.Millisecond,
		blackThinkTime: 1000 * time.Millisecond,
	}
	entry.stopSearch.Store(false)
	s.mu.Lock()
	s.games[id] = entry
	s.mu.Unlock()
	go s.maybeTriggerEngine(entry)
	return entry, id, nil
}

func (s *Server) syncGameToDB(entry *gameEntry, newAssess any) {
	s.mu.Lock()
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
		ID:          entry.id,
		UserID:      entry.userID,
		SessionID:   entry.sessionID,
		FEN:         gm.Board.FEN(),
		History:     string(hist),
		HistorySAN:  string(histSAN),
		EngineWhite: gm.EngineWhite,
		EngineBlack: gm.EngineBlack,
		Status:      string(status),
		Assessments: string(assessJSON),
		CreatedAt:   entry.createdAt,
		UpdatedAt:   time.Now(),
	}
	snapshot := s.snapshotLocked(entry)
	snapshot.Assessments = assessments

	// Detect game end and publish event
	shouldEmit := status != game.StatusOngoing && !entry.eventFired.Load()
	if shouldEmit {
		entry.eventFired.Store(true)
	}
	s.mu.Unlock()

	s.db.SaveGame(gameRec)
	s.hub.BroadcastState(entry.id, snapshot)

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

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
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
	http.SetCookie(w, &http.Cookie{Name: "token", Value: token, Path: "/", HttpOnly: true})
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
	http.SetCookie(w, &http.Cookie{Name: "token", Value: token, Path: "/", HttpOnly: true})
	writeJSON(w, map[string]any{"user": user, "token": token})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "token", Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(204)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	writeJSON(w, user)
}

// Game Management Handlers

func (s *Server) handleCreateGame(w http.ResponseWriter, r *http.Request) {
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
		game:           gm,
		id:             id,
		userID:         userID,
		sessionID:      sessionID,
		createdAt:      time.Now(),
		lastUsed:       time.Now(),
		whiteThinkTime: 1000 * time.Millisecond,
		blackThinkTime: 1000 * time.Millisecond,
	}
	entry.stopSearch.Store(false)
	s.mu.Lock()
	s.games[id] = entry
	s.mu.Unlock()
	
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
	user, _ := auth.GetUser(r.Context())
	sessionID := auth.GetSessionID(r.Context())

	s.mu.Lock()
	entry, ok := s.games[id]
	if ok {
		authorized := false
		if (user != nil && entry.userID == user.UserID) || (entry.userID == 0 && entry.sessionID == sessionID) {
			authorized = true
		}
		if !authorized {
			s.mu.Unlock()
			http.Error(w, "unauthorized", 401)
			return
		}
		entry.stopSearch.Store(true)
		delete(s.games, id)
	}
	s.mu.Unlock()

	var userID int64
	if user != nil {
		userID = user.UserID
	}
	err := s.db.DeleteGame(id, userID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

// Play Handlers

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	entry, id, err := s.getGame(r)
	if err != nil {
		slog.Error("handleState error", "error", err, "game_id", id)
		http.Error(w, err.Error(), 404)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, s.snapshotLocked(entry))
}

func (s *Server) handleTouch(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct{ Square string `json:"square"` }
	json.NewDecoder(r.Body).Decode(&req)
	sq, _ := core.ParseSquare(req.Square)
	s.mu.Lock()
	entry.game.Touch(sq)
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()
	writeJSON(w, snapshot)
}

func (s *Server) handleTouchMove(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct{ Enabled bool `json:"enabled"` }
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	entry.game.TouchMove = req.Enabled
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()
	writeJSON(w, snapshot)
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct{ Move string `json:"move"` }
	json.NewDecoder(r.Body).Decode(&req)
	
	s.mu.Lock()
	if s.isThinking(r.Context(), entry.id) {
		s.mu.Unlock()
		http.Error(w, "engine is thinking", 409)
		return
	}
	if entry.game.Status() != game.StatusOngoing {
		s.mu.Unlock()
		http.Error(w, "game is already finished", 403)
		return
	}
	if entry.game.EngineToMove() {
		s.mu.Unlock()
		http.Error(w, "it is the engine's turn", 403)
		return
	}
	if entry.game.TouchLost {
		s.mu.Unlock()
		http.Error(w, "touch-move violation: game lost", 403)
		return
	}

	m, err := entry.game.Board.ParseUCIMove(req.Move)
	if err != nil {
		s.mu.Unlock()
		http.Error(w, "invalid move format", 400)
		return
	}
	matched, ok := game.MatchMove(entry.game.Board.GenerateLegalMoves(), m)
	if !ok {
		s.mu.Unlock()
		http.Error(w, "illegal move", 403)
		return
	}
	
	entry.game.PlayMove(matched)
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()
	
	go s.syncGameToDB(entry, nil)
	go s.maybeTriggerEngine(entry)
	writeJSON(w, snapshot)
}

func (s *Server) broadcastEngineAbort(gameID string) {
	slog.Info("broadcasting engine abort", "game_id", gameID)
	abort := bus.EngineAbort{GameID: gameID}
	if err := s.bus.Publish(context.Background(), bus.EngineAbortChannel, abort); err != nil {
		slog.Error("failed to publish engine abort", "error", err)
	}
}

func (s *Server) handleNew(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		EngineWhite bool `json:"engine_white"`
		EngineBlack bool `json:"engine_black"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	
	s.mu.Lock()
	// Signal any current search to stop
	entry.stopSearch.Store(true)
	s.broadcastEngineAbort(entry.id)
	
	entry.game.Reset()
	entry.game.EngineWhite, entry.game.EngineBlack = req.EngineWhite, req.EngineBlack
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()

	go s.syncGameToDB(entry, nil)
	go s.maybeTriggerEngine(entry)
	writeJSON(w, snapshot)
}

func (s *Server) handleSetPlayers(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		EngineWhite    bool `json:"engine_white"`
		EngineBlack    bool `json:"engine_black"`
		WhiteThinkTime int  `json:"white_think_time"`
		BlackThinkTime int  `json:"black_think_time"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	
	s.mu.Lock()
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
	s.mu.Unlock()
	go s.syncGameToDB(entry, nil)
	go s.maybeTriggerEngine(entry)
	writeJSON(w, snapshot)
}

func (s *Server) handleUndo(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.mu.Lock()
	entry.stopSearch.Store(true)
	s.broadcastEngineAbort(entry.id)
	entry.game.Undo()
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()
	go s.syncGameToDB(entry, nil)
	go s.maybeTriggerEngine(entry)
	writeJSON(w, snapshot)
}

func (s *Server) handleHint(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct{ MoveTime int `json:"movetime"` }
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	if s.isThinking(r.Context(), entry.id) {
		s.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	board := *entry.game.Board
	hist := game.CopyHistory(entry.game.HistoryHash())
	moveTime := time.Duration(req.MoveTime) * time.Millisecond

	s.setThinking(context.Background(), entry.id, true)
	entry.stopSearch.Store(false)
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()

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
		if err := s.bus.Publish(context.Background(), bus.EngineRequestChannel, job); err != nil {
			slog.Error("failed to dispatch hint request", "error", err)
			s.mu.Lock()
			s.setThinking(context.Background(), entry.id, false)
			s.mu.Unlock()
			s.syncGameToDB(entry, nil)
		}
	}()

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleAssess(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		MoveTime int  `json:"movetime"`
		Index    *int `json:"index"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	if s.isThinking(r.Context(), entry.id) {
		s.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	idx := entry.game.LastHumanMoveIndex()
	if req.Index != nil {
		idx = *req.Index
	}
	if idx < 0 || idx >= len(entry.game.UndoStack) {
		s.mu.Unlock()
		http.Error(w, "no move to assess", 400)
		return
	}
	before, _ := entry.game.BoardsAroundMove(idx)
	userMove, _ := entry.game.UndoStack[idx].Move, entry.game.PlayerAt(idx)
	moveTime := time.Duration(req.MoveTime) * time.Millisecond

	s.setThinking(context.Background(), entry.id, true)
	entry.stopSearch.Store(false)
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()

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
		if err := s.bus.Publish(context.Background(), bus.EngineRequestChannel, job); err != nil {
			slog.Error("failed to dispatch assess request", "error", err)
			s.mu.Lock()
			s.setThinking(context.Background(), entry.id, false)
			s.mu.Unlock()
			s.syncGameToDB(entry, nil)
		}
	}()

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
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

	s.mu.Lock()
	defer s.mu.Unlock()
	
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
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var sg struct { StartFEN string; Moves []string; EngineWhite, EngineBlack bool }
	json.NewDecoder(r.Body).Decode(&sg)
	s.mu.Lock()
	entry.stopSearch.Store(true)
	s.broadcastEngineAbort(entry.id)
	entry.game.Load(sg.StartFEN, sg.Moves, sg.EngineWhite, sg.EngineBlack)
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()
	go s.syncGameToDB(entry, nil)
	go s.maybeTriggerEngine(entry)
	writeJSON(w, snapshot)
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(entry.game.ReplayData())
	html := bytes.Replace(replayHTML, []byte(replayPlaceholder), data, 1)
	w.Header().Set("Content-Type", "text/html")
	w.Write(html)
}

// Infrastructure Handlers

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"status": "ok", "time": time.Now().Format(time.RFC3339)})
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
	s.mu.Lock()
	s.lastPing = time.Now()
	s.mu.Unlock()
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

		s.mu.Lock()
		entry, ok := s.games[resp.GameID]
		if !ok {
			s.mu.Unlock()
			return
		}

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
			s.mu.Unlock()
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
			s.mu.Unlock()
			go s.syncGameToDB(entry, nil)
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
			s.mu.Unlock()
			go s.syncGameToDB(entry, assessPayload)
			return
		}

		s.setThinking(context.Background(), entry.id, false)
		s.mu.Unlock()
	})
}

func (s *Server) maybeTriggerEngine(entry *gameEntry) {
	s.mu.Lock()
	if s.isThinking(context.Background(), entry.id) {
		s.mu.Unlock()
		return
	}

	if !entry.game.EngineToMove() || entry.game.Status() != game.StatusOngoing {
		s.mu.Unlock()
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
	s.mu.Unlock()

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
		if err := s.bus.Publish(context.Background(), bus.EngineRequestChannel, req); err != nil {
			slog.Error("failed to dispatch engine request", "error", err)
			s.mu.Lock()
			s.setThinking(context.Background(), entry.id, false)
			s.mu.Unlock()
			s.syncGameToDB(entry, nil)
		} else {
			slog.Info("engine request dispatched to worker", "game_id", entry.id, "movetime", moveTime)
		}
	}()
}

func (s *Server) snapshotLocked(entry *gameEntry) stateJSON {
	game := entry.game
	var lm *moveJSON
	if game.LastMove != nil { lm = &moveJSON{From: core.SquareName(game.LastMove.From), To: core.SquareName(game.LastMove.To)} }
	legal := game.Board.GenerateLegalMoves()
	legalStrs := make([]string, len(legal))
	for i, m := range legal { legalStrs[i] = m.String() }
	turn := "w"
	if game.Board.SideToMove == core.Black { turn = "b" }
	history, historySAN := append([]string(nil), game.History...), append([]string(nil), game.HistorySAN...)
	if history == nil { history = []string{} }
	if historySAN == nil { historySAN = []string{} }
	
	var assessments []any
	record, err := s.db.GetGame(entry.id)
	if err == nil && record.Assessments != "" {
		json.Unmarshal([]byte(record.Assessments), &assessments)
	}
	if assessments == nil { assessments = []any{} }

	return stateJSON{
		FEN: game.Board.FEN(), Turn: turn, EngineWhite: game.EngineWhite, EngineBlack: game.EngineBlack,
		EngineToMove: game.EngineToMove(), Status: string(game.Status()), InCheck: game.Board.InCheck(game.Board.SideToMove),
		LegalMoves: legalStrs, History: history, HistorySAN: historySAN, LastMove: lm, 
		Thinking: s.isThinking(context.Background(), entry.id),
		TouchMove: game.TouchMove, TouchedSquare: core.SquareName(game.TouchedSq),
		WhiteThinkTime: int(entry.whiteThinkTime / time.Millisecond),
		BlackThinkTime: int(entry.blackThinkTime / time.Millisecond),
		WhiteTime:      game.WhiteTime,
		BlackTime:      game.BlackTime,
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
	WhiteTime      int64     `json:"white_time"`
	BlackTime      int64     `json:"black_time"`
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
