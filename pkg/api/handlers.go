package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/game"
	"github.com/neoromantics/chess/pkg/uci"
)

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

func (s *Server) syncGameToDB(entry *gameEntry) {
	s.mu.Lock()
	gm := entry.game
	hist, _ := json.Marshal(gm.History)
	histSAN, _ := json.Marshal(gm.HistorySAN)
	record := &db.GameRecord{
		ID:          entry.id,
		UserID:      entry.userID,
		SessionID:   entry.sessionID,
		FEN:         gm.Board.FEN(),
		History:     string(hist),
		HistorySAN:  string(histSAN),
		EngineWhite: gm.EngineWhite,
		EngineBlack: gm.EngineBlack,
		Status:      string(gm.Status()),
		CreatedAt:   entry.createdAt,
		UpdatedAt:   time.Now(),
	}
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()

	s.db.SaveGame(record)
	s.hub.BroadcastState(entry.id, snapshot)
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
	
	s.syncGameToDB(entry)
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
	if entry.thinking.Load() {
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
	
	go s.syncGameToDB(entry)
	go s.maybeTriggerEngine(entry)
	writeJSON(w, snapshot)
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
	
	entry.game.Reset()
	entry.game.EngineWhite, entry.game.EngineBlack = req.EngineWhite, req.EngineBlack
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()

	go s.syncGameToDB(entry)
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
	
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()
	go s.syncGameToDB(entry)
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
	entry.game.Undo()
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()
	go s.syncGameToDB(entry)
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
	if entry.thinking.Load() {
		s.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	board := *entry.game.Board
	hist := game.CopyHistory(entry.game.HistoryHash())
	entry.thinking.Store(true)
	entry.stopSearch.Store(false)
	s.mu.Unlock()
	
	defer entry.thinking.Store(false)
	result := board.IterativeDeepening(core.SearchLimits{MoveTime: time.Duration(req.MoveTime) * time.Millisecond, History: hist}, &entry.stopSearch, nil)
	
	s.mu.Lock()
	defer s.mu.Unlock()
	res := map[string]any{"state": s.snapshotLocked(entry)}
	if result.BestMove != (core.Move{}) && !entry.stopSearch.Load() {
		m := result.BestMove
		res["hint"] = map[string]any{
			"move": m.String(), "from": core.SquareName(m.From), "to": core.SquareName(m.To),
			"promo": string(core.PromoChar(m.Promo)), "score": uci.ScoreToUCI(result.Score), "depth": result.Depth,
		}
	}
	writeJSON(w, res)
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
	if entry.thinking.Load() {
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
	before, after := entry.game.BoardsAroundMove(idx)
	userMove, player := entry.game.UndoStack[idx].Move, entry.game.PlayerAt(idx)
	entry.thinking.Store(true)
	entry.stopSearch.Store(false)
	s.mu.Unlock()
	
	defer entry.thinking.Store(false)
	t := time.Duration(req.MoveTime) * time.Millisecond
	resBefore := before.IterativeDeepening(core.SearchLimits{MoveTime: t}, &entry.stopSearch, nil)
	resAfter := after.IterativeDeepening(core.SearchLimits{MoveTime: t}, &entry.stopSearch, nil)
	
	s.mu.Lock()
	defer s.mu.Unlock()
	bestScore, userScore := resBefore.Score, resAfter.Score
	if player == core.Black { bestScore, userScore = -bestScore, -userScore }
	cpLoss := bestScore - userScore
	writeJSON(w, map[string]any{
		"index": idx, "label": game.ClassifyAssessment(userMove, resBefore.BestMove, cpLoss, bestScore, userScore),
		"move": userMove.String(), "best_move": resBefore.BestMove.String(),
		"user_score": uci.ScoreToUCI(resAfter.Score), "best_score": uci.ScoreToUCI(resBefore.Score), "cp_loss": cpLoss,
	})
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	entry, _, err := s.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Disposition", "attachment; filename=chess-game.json")
	writeJSON(w, map[string]any{"start_fen": entry.game.StartFEN, "moves": entry.game.History, "engine_white": entry.game.EngineWhite, "engine_black": entry.game.EngineBlack})
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
	entry.game.Load(sg.StartFEN, sg.Moves, sg.EngineWhite, sg.EngineBlack)
	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()
	go s.syncGameToDB(entry)
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

func (s *Server) maybeTriggerEngine(entry *gameEntry) {
	s.mu.Lock()
	if entry.thinking.Load() {
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
	
	entry.thinking.Store(true)
	entry.stopSearch.Store(false)

	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()

	s.hub.BroadcastState(entry.id, snapshot)

	go func(e *gameEntry, b core.Board, h map[uint64]int, t time.Duration) {
		slog.Info("engine starting calculation", "game_id", e.id, "movetime", t)
		
		// ALWAYS ensure thinking is cleared
		defer e.thinking.Store(false)
		
		// Safety: ensure a re-check happens after this goroutine exits
		// to handle settings changes or subsequent moves.
		defer func() {
			time.AfterFunc(100*time.Millisecond, func() {
				s.maybeTriggerEngine(e)
			})
		}()

		result := b.IterativeDeepening(
			core.SearchLimits{MoveTime: t, History: h},
			&e.stopSearch,
			nil,
		)

		s.mu.Lock()
		if e.stopSearch.Load() || !e.game.EngineToMove() {
			slog.Info("engine search aborted or settings changed", "game_id", e.id)
			s.mu.Unlock()
			// Use a specialized sync to avoid deadlock
			s.syncGameToDB(e) 
			return
		}

		if result.BestMove != (core.Move{}) {
			if matched, ok := game.MatchMove(e.game.Board.GenerateLegalMoves(), result.BestMove); ok {
				slog.Info("engine playing move", "game_id", e.id, "move", matched.String())
				e.game.PlayMove(matched)
			}
		}
		s.mu.Unlock()

		s.syncGameToDB(e)
	}(entry, board, hist, moveTime)
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
	return stateJSON{
		FEN: game.Board.FEN(), Turn: turn, EngineWhite: game.EngineWhite, EngineBlack: game.EngineBlack,
		EngineToMove: game.EngineToMove(), Status: string(game.Status()), InCheck: game.Board.InCheck(game.Board.SideToMove),
		LegalMoves: legalStrs, History: history, HistorySAN: historySAN, LastMove: lm, Thinking: entry.thinking.Load(),
		TouchMove: game.TouchMove, TouchedSquare: core.SquareName(game.TouchedSq),
		WhiteThinkTime: int(entry.whiteThinkTime / time.Millisecond),
		BlackThinkTime: int(entry.blackThinkTime / time.Millisecond),
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
}

type moveJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
