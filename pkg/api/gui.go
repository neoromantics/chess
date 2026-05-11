package api

import (
	"bytes"
	"embed"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/taiyanliu/chess/pkg/auth"
	"github.com/taiyanliu/chess/pkg/core"
	"github.com/taiyanliu/chess/pkg/db"
	"github.com/taiyanliu/chess/pkg/game"
	"github.com/taiyanliu/chess/pkg/uci"
)

//go:embed all:dist
var frontendDist embed.FS

var (
	guiHTML    []byte
	replayHTML []byte
	assetsFS   http.FileSystem
)

func init() {
	var err error
	guiHTML, err = frontendDist.ReadFile("dist/index.html")
	if err != nil {
		panic(fmt.Sprintf("failed to read index.html: %v", err))
	}
	replayHTML, err = frontendDist.ReadFile("dist/replay.html")
	if err != nil {
		panic(fmt.Sprintf("failed to read replay.html: %v", err))
	}
	sub, err := fs.Sub(frontendDist, "dist")
	if err != nil {
		panic(err)
	}
	assetsFS = http.FS(sub)
}

const replayPlaceholder = "REPLAY_DATA_PLACEHOLDER"

type gameEntry struct {
	game      *game.Game
	thinking  atomic.Bool
	lastUsed  time.Time
	id        string
	userID    int64
	createdAt time.Time
}

type GUI struct {
	mux *http.ServeMux
	db  *db.DB

	mu    sync.Mutex
	games map[string]*gameEntry

	lastPing time.Time
}

func NewGUI(database *db.DB) *GUI {
	g := &GUI{
		db:       database,
		games:    make(map[string]*gameEntry),
		lastPing: time.Now(),
	}
	g.mux = http.NewServeMux()
	g.registerRoutes()
	return g
}

func (g *GUI) StartIdleShutdown(d time.Duration) {
	go func() {
		for {
			time.Sleep(5 * time.Second)
			g.mu.Lock()
			idle := time.Since(g.lastPing)
			g.mu.Unlock()
			if idle > d {
				// os.Exit(0) removed for web safety; would use a shutdown channel in prod
			}
		}
	}()
}

func (g *GUI) registerRoutes() {
	// Auth routes
	g.mux.HandleFunc("POST /api/auth/signup", g.handleSignup)
	g.mux.HandleFunc("POST /api/auth/login", g.handleLogin)
	g.mux.HandleFunc("POST /api/auth/logout", g.handleLogout)
	g.mux.HandleFunc("GET /api/user/me", g.handleMe)

	// Game management
	g.mux.HandleFunc("POST /api/games/new", g.handleCreateGame)
	g.mux.HandleFunc("GET /api/games", g.handleListGames)
	g.mux.HandleFunc("DELETE /api/games/delete", g.handleDeleteGame)

	// Single game routes (all require game_id)
	g.mux.HandleFunc("GET /api/state", g.handleState)
	g.mux.HandleFunc("POST /api/move", g.handleMove)
	g.mux.HandleFunc("POST /api/new", g.handleNew)
	g.mux.HandleFunc("POST /api/engine_step", g.handleEngineStep)
	g.mux.HandleFunc("POST /api/hint", g.handleHint)
	g.mux.HandleFunc("POST /api/assess", g.handleAssess)
	g.mux.HandleFunc("POST /api/set_players", g.handleSetPlayers)
	g.mux.HandleFunc("POST /api/touch", g.handleTouch)
	g.mux.HandleFunc("POST /api/touch_move", g.handleTouchMove)
	g.mux.HandleFunc("POST /api/undo", g.handleUndo)
	g.mux.HandleFunc("POST /api/ping", g.handlePing)
	g.mux.HandleFunc("GET /api/save", g.handleSave)
	g.mux.HandleFunc("POST /api/load", g.handleLoad)
	g.mux.HandleFunc("GET /api/replay.html", g.handleReplay)

	// Static assets
	g.mux.Handle("GET /assets/", http.FileServer(assetsFS))
	g.mux.HandleFunc("GET /{$}", g.handleIndex)
}

func (g *GUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")

	if r.Method == "OPTIONS" {
		return
	}

	auth.Middleware(g.mux).ServeHTTP(w, r)
}

func (g *GUI) getGame(r *http.Request) (*gameEntry, string, error) {
	id := r.URL.Query().Get("game_id")
	if id == "" {
		return nil, "", fmt.Errorf("missing game_id")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	entry, ok := g.games[id]
	if ok {
		entry.lastUsed = time.Now()
		return entry, id, nil
	}

	// Try to load from DB if not in memory
	record, err := g.db.GetGame(id)
	if err != nil {
		return nil, "", fmt.Errorf("game not found")
	}

	// Verify ownership if logged in
	user, ok := auth.GetUser(r.Context())
	if ok && record.UserID != user.UserID {
		return nil, "", fmt.Errorf("unauthorized")
	}

	gameInst := game.NewGame()
	var history, historySAN []string
	json.Unmarshal([]byte(record.History), &history)
	json.Unmarshal([]byte(record.HistorySAN), &historySAN)
	gameInst.Load(record.FEN, history, record.EngineWhite, record.EngineBlack)
	gameInst.HistorySAN = historySAN // Load doesn't handle SAN history perfectly

	entry = &gameEntry{
		game:      gameInst,
		id:        id,
		userID:    record.UserID,
		createdAt: record.CreatedAt,
		lastUsed:  time.Now(),
	}
	g.games[id] = entry
	return entry, id, nil
}

func (g *GUI) syncGameToDB(entry *gameEntry) {
	g.mu.Lock()
	gm := entry.game
	hist, _ := json.Marshal(gm.History)
	histSAN, _ := json.Marshal(gm.HistorySAN)
	record := &db.GameRecord{
		ID:          entry.id,
		UserID:      entry.userID,
		FEN:         gm.Board.FEN(),
		History:     string(hist),
		HistorySAN:  string(histSAN),
		EngineWhite: gm.EngineWhite,
		EngineBlack: gm.EngineBlack,
		Status:      string(gm.Status()),
		CreatedAt:   entry.createdAt,
		UpdatedAt:   time.Now(),
	}
	g.mu.Unlock()
	g.db.SaveGame(record)
}

func (g *GUI) handleSignup(w http.ResponseWriter, r *http.Request) {
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

	user, err := g.db.CreateUser(req.Username, hash)
	if err != nil {
		http.Error(w, "username taken", 409)
		return
	}

	token, _ := auth.GenerateToken(user.ID, user.Username)
	http.SetCookie(w, &http.Cookie{Name: "token", Value: token, Path: "/", HttpOnly: true})
	writeJSON(w, map[string]any{"user": user, "token": token})
}

func (g *GUI) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	user, err := g.db.GetUserByUsername(req.Username)
	if err != nil || !auth.CheckPasswordHash(req.Password, user.PasswordHash) {
		http.Error(w, "invalid credentials", 401)
		return
	}

	token, _ := auth.GenerateToken(user.ID, user.Username)
	http.SetCookie(w, &http.Cookie{Name: "token", Value: token, Path: "/", HttpOnly: true})
	writeJSON(w, map[string]any{"user": user, "token": token})
}

func (g *GUI) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "token", Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(204)
}

func (g *GUI) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	writeJSON(w, user)
}

func (g *GUI) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())
	var userID int64
	if user != nil {
		userID = user.UserID
	}

	id := uuid.New().String()
	entry := &gameEntry{
		game:      game.NewGame(),
		id:        id,
		userID:    userID,
		createdAt: time.Now(),
		lastUsed:  time.Now(),
	}
	g.mu.Lock()
	g.games[id] = entry
	g.mu.Unlock()
	
	g.syncGameToDB(entry)
	writeJSON(w, map[string]string{"game_id": id})
}

func (g *GUI) handleListGames(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		// For guest users, only return what's in memory for their session
		// (Ideally guests would have a session ID, but let's keep it simple)
		g.mu.Lock()
		defer g.mu.Unlock()
		ids := make([]string, 0, len(g.games))
		for id := range g.games {
			ids = append(ids, id)
		}
		writeJSON(w, ids)
		return
	}

	records, err := g.db.ListGames(user.UserID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, records)
}

func (g *GUI) handleDeleteGame(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("game_id")
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}

	g.mu.Lock()
	delete(g.games, id)
	g.mu.Unlock()

	err := g.db.DeleteGame(id, user.UserID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

func (g *GUI) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write(guiHTML)
}

func (g *GUI) handlePing(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.lastPing = time.Now()
	g.mu.Unlock()
	w.WriteHeader(204)
}

func (g *GUI) handleState(w http.ResponseWriter, r *http.Request) {
	entry, id, err := g.getGame(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "handleState error: %v\n", err)
		http.Error(w, err.Error(), 404)
		return
	}
	fmt.Printf("Fetching state for game %s\n", id)
	g.mu.Lock()
	snapshot := g.snapshotLocked(entry)
	g.mu.Unlock()
	writeJSON(w, snapshot)
}

func (g *GUI) handleTouch(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		Square string `json:"square"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	sq, err := core.ParseSquare(req.Square)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	entry.game.Touch(sq)
	g.mu.Unlock()
	writeJSON(w, g.snapshotLocked(entry))
}

func (g *GUI) handleTouchMove(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	entry.game.TouchMove = req.Enabled
	g.mu.Unlock()
	writeJSON(w, g.snapshotLocked(entry))
}

func (g *GUI) handleMove(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		Move string `json:"move"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	if entry.thinking.Load() || entry.game.TouchLost || entry.game.EngineToMove() {
		g.mu.Unlock()
		http.Error(w, "busy or not your turn", 409)
		return
	}
	m, err := entry.game.Board.ParseUCIMove(req.Move)
	if err != nil {
		g.mu.Unlock()
		http.Error(w, err.Error(), 400)
		return
	}
	matched, ok := game.MatchMove(entry.game.Board.GenerateLegalMoves(), m)
	if !ok {
		g.mu.Unlock()
		http.Error(w, "illegal move", 403)
		return
	}
	entry.game.PlayMove(matched)
	g.mu.Unlock()
	
	g.syncGameToDB(entry)
	writeJSON(w, g.snapshotLocked(entry))
}

func (g *GUI) handleNew(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		EngineWhite bool `json:"engine_white"`
		EngineBlack bool `json:"engine_black"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	if entry.thinking.Load() {
		g.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	entry.game.Reset()
	entry.game.EngineWhite = req.EngineWhite
	entry.game.EngineBlack = req.EngineBlack
	g.mu.Unlock()

	g.syncGameToDB(entry)
	writeJSON(w, g.snapshotLocked(entry))
}

func (g *GUI) handleEngineStep(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		MoveTime int `json:"movetime"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	if entry.thinking.Load() || !entry.game.EngineToMove() || entry.game.Status() != game.StatusOngoing {
		g.mu.Unlock()
		http.Error(w, "busy or not engine turn", 409)
		return
	}
	board := *entry.game.Board
	hist := game.CopyHistory(entry.game.HistoryHash())
	entry.thinking.Store(true)
	g.mu.Unlock()

	defer entry.thinking.Store(false)

	result := board.IterativeDeepening(
		core.SearchLimits{MoveTime: time.Duration(req.MoveTime) * time.Millisecond, History: hist},
		&atomic.Bool{},
		nil,
	)

	g.mu.Lock()
	if result.BestMove != (core.Move{}) {
		if matched, ok := game.MatchMove(entry.game.Board.GenerateLegalMoves(), result.BestMove); ok {
			entry.game.PlayMove(matched)
		}
	}
	g.mu.Unlock()
	
	g.syncGameToDB(entry)
	writeJSON(w, g.snapshotLocked(entry))
}

func (g *GUI) handleHint(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		MoveTime int `json:"movetime"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	if entry.thinking.Load() {
		g.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	board := *entry.game.Board
	hist := game.CopyHistory(entry.game.HistoryHash())
	entry.thinking.Store(true)
	g.mu.Unlock()

	defer entry.thinking.Store(false)

	result := board.IterativeDeepening(
		core.SearchLimits{MoveTime: time.Duration(req.MoveTime) * time.Millisecond, History: hist},
		&atomic.Bool{},
		nil,
	)

	g.mu.Lock()
	defer g.mu.Unlock()
	res := map[string]any{"state": g.snapshotLocked(entry)}
	if result.BestMove != (core.Move{}) {
		m := result.BestMove
		res["hint"] = map[string]any{
			"move":  m.String(),
			"from":  core.SquareName(m.From),
			"to":    core.SquareName(m.To),
			"promo": string(core.PromoChar(m.Promo)),
			"score": uci.ScoreToUCI(result.Score),
			"depth": result.Depth,
		}
	}
	writeJSON(w, res)
}

func (g *GUI) handleAssess(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		MoveTime int  `json:"movetime"`
		Index    *int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	if entry.thinking.Load() {
		g.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	idx := entry.game.LastHumanMoveIndex()
	if req.Index != nil {
		idx = *req.Index
	}
	if idx < 0 || idx >= len(entry.game.UndoStack) {
		g.mu.Unlock()
		http.Error(w, "no move to assess", 400)
		return
	}
	before, after := entry.game.BoardsAroundMove(idx)
	userMove := entry.game.UndoStack[idx].Move
	player := entry.game.PlayerAt(idx)
	entry.thinking.Store(true)
	g.mu.Unlock()

	defer entry.thinking.Store(false)

	stop1, stop2 := &atomic.Bool{}, &atomic.Bool{}
	t := time.Duration(req.MoveTime) * time.Millisecond
	resBefore := before.IterativeDeepening(core.SearchLimits{MoveTime: t}, stop1, nil)
	resAfter := after.IterativeDeepening(core.SearchLimits{MoveTime: t}, stop2, nil)

	bestScore, userScore := resBefore.Score, resAfter.Score
	if player == core.Black {
		bestScore, userScore = -bestScore, -userScore
	}
	cpLoss := bestScore - userScore

	writeJSON(w, map[string]any{
		"index":      idx,
		"label":      game.ClassifyAssessment(userMove, resBefore.BestMove, cpLoss, bestScore, userScore),
		"move":       userMove.String(),
		"best_move":  resBefore.BestMove.String(),
		"user_score": uci.ScoreToUCI(resAfter.Score),
		"best_score": uci.ScoreToUCI(resBefore.Score),
		"cp_loss":    cpLoss,
	})
}

func (g *GUI) handleSetPlayers(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var req struct {
		EngineWhite bool `json:"engine_white"`
		EngineBlack bool `json:"engine_black"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	g.mu.Lock()
	entry.game.EngineWhite = req.EngineWhite
	entry.game.EngineBlack = req.EngineBlack
	g.mu.Unlock()
	
	g.syncGameToDB(entry)
	writeJSON(w, g.snapshotLocked(entry))
}

func (g *GUI) handleUndo(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	g.mu.Lock()
	if entry.thinking.Load() {
		g.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	entry.game.Undo()
	g.mu.Unlock()

	g.syncGameToDB(entry)
	writeJSON(w, g.snapshotLocked(entry))
}

func (g *GUI) handleSave(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	w.Header().Set("Content-Disposition", "attachment; filename=chess-game.json")
	writeJSON(w, map[string]any{
		"start_fen":    entry.game.StartFEN,
		"moves":       entry.game.History,
		"engine_white": entry.game.EngineWhite,
		"engine_black": entry.game.EngineBlack,
	})
}

func (g *GUI) handleLoad(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var sg struct {
		StartFEN    string   `json:"start_fen"`
		Moves       []string `json:"moves"`
		EngineWhite bool     `json:"engine_white"`
		EngineBlack bool     `json:"engine_black"`
	}
	json.NewDecoder(r.Body).Decode(&sg)
	g.mu.Lock()
	defer g.mu.Unlock()
	entry.game.Load(sg.StartFEN, sg.Moves, sg.EngineWhite, sg.EngineBlack)
	g.mu.Unlock()

	g.syncGameToDB(entry)
	writeJSON(w, g.snapshotLocked(entry))
}

func (g *GUI) handleReplay(w http.ResponseWriter, r *http.Request) {
	entry, _, err := g.getGame(r)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	data, _ := json.Marshal(entry.game.ReplayData())
	html := bytes.Replace(replayHTML, []byte(replayPlaceholder), data, 1)
	w.Header().Set("Content-Type", "text/html")
	w.Write(html)
}

func (g *GUI) snapshotLocked(entry *gameEntry) stateJSON {
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
	history := append([]string(nil), game.History...)
	if history == nil {
		history = []string{}
	}
	historySAN := append([]string(nil), game.HistorySAN...)
	if historySAN == nil {
		historySAN = []string{}
	}
	return stateJSON{
		FEN:           game.Board.FEN(),
		Turn:          turn,
		EngineWhite:   game.EngineWhite,
		EngineBlack:   game.EngineBlack,
		EngineToMove:  game.EngineToMove(),
		Status:        string(game.Status()),
		InCheck:       game.Board.InCheck(game.Board.SideToMove),
		LegalMoves:    legalStrs,
		History:       history,
		HistorySAN:    historySAN,
		LastMove:      lm,
		Thinking:      entry.thinking.Load(),
		TouchMove:     game.TouchMove,
		TouchedSquare: core.SquareName(game.TouchedSq),
	}
}

type stateJSON struct {
	FEN           string    `json:"fen"`
	Turn          string    `json:"turn"`
	EngineWhite   bool      `json:"engine_white"`
	EngineBlack   bool      `json:"engine_black"`
	EngineToMove  bool      `json:"engine_to_move"`
	Status        string    `json:"status"`
	InCheck       bool      `json:"in_check"`
	LegalMoves    []string  `json:"legal_moves"`
	History       []string  `json:"history"`
	HistorySAN    []string  `json:"history_san"`
	LastMove      *moveJSON `json:"last_move"`
	Thinking      bool      `json:"thinking"`
	TouchMove     bool      `json:"touch_move"`
	TouchedSquare string    `json:"touched_square"`
}

type moveJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
