package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed gui.html
var guiHTML []byte

//go:embed replay.html
var replayHTML []byte

const replayPlaceholder = "REPLAY_DATA_PLACEHOLDER"

// GUI is a thin HTTP shim over Game. It owns concurrency (mu) and search
// lifecycle (thinking flag); all chess logic lives in Game.
type GUI struct {
	mu       sync.Mutex
	game     *Game
	thinking bool
	mux      *http.ServeMux

	// lastPing is updated by /api/ping. The optional idle-shutdown
	// goroutine (started via startIdleShutdown) reads it under mu.
	lastPing time.Time
}

func NewGUI() *GUI {
	g := &GUI{game: NewGame(), lastPing: time.Now()}
	g.game.EngineBlack = true // default: human plays White, engine plays Black
	g.mux = http.NewServeMux()
	g.registerRoutes()
	return g
}

// startIdleShutdown launches a goroutine that exits the process if no
// /api/ping arrives within the given timeout. Used for .app bundle
// launches so closing the browser tab quits the app cleanly.
func (g *GUI) startIdleShutdown(timeout time.Duration) {
	go func() {
		check := timeout / 6
		if check < time.Second {
			check = time.Second
		}
		ticker := time.NewTicker(check)
		defer ticker.Stop()
		for range ticker.C {
			g.mu.Lock()
			elapsed := time.Since(g.lastPing)
			g.mu.Unlock()
			if elapsed > timeout {
				fmt.Fprintf(os.Stderr, "no browser ping for %v; shutting down\n", timeout)
				os.Exit(0)
			}
		}
	}()
}

// registerRoutes wires every endpoint into the mux. Method-prefixed
// patterns (Go 1.22+) keep GETs and POSTs distinct, and `/{$}` matches
// only the bare root so unknown paths fall through to the default 404.
func (g *GUI) registerRoutes() {
	g.mux.HandleFunc("GET /{$}", g.handleIndex)
	g.mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { g.handleState(w) })
	g.mux.HandleFunc("POST /api/move", g.handleMove)
	g.mux.HandleFunc("POST /api/new", g.handleNew)
	g.mux.HandleFunc("POST /api/engine_step", g.handleEngineStep)
	g.mux.HandleFunc("POST /api/hint", g.handleHint)
	g.mux.HandleFunc("POST /api/touch", g.handleTouch)
	g.mux.HandleFunc("POST /api/touch_move", g.handleTouchMove)
	g.mux.HandleFunc("POST /api/assess", g.handleAssess)
	g.mux.HandleFunc("POST /api/set_players", g.handleSetPlayers)
	g.mux.HandleFunc("GET /api/save", func(w http.ResponseWriter, r *http.Request) { g.handleSave(w) })
	g.mux.HandleFunc("POST /api/load", g.handleLoad)
	g.mux.HandleFunc("POST /api/undo", func(w http.ResponseWriter, r *http.Request) { g.handleUndo(w) })
	g.mux.HandleFunc("GET /api/replay.html", g.handleReplay)
	g.mux.HandleFunc("POST /api/ping", g.handlePing)
}

func (g *GUI) handlePing(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.lastPing = time.Now()
	g.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (g *GUI) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(guiHTML)
}

// JSON shapes ---------------------------------------------------------------

type stateJSON struct {
	FEN           string    `json:"fen"`
	Turn          string    `json:"turn"`
	EngineWhite   bool      `json:"engine_white"`
	EngineBlack   bool      `json:"engine_black"`
	EngineToMove  bool      `json:"engine_to_move"`
	Status        string    `json:"status"`
	InCheck       bool      `json:"in_check"`
	LegalMoves    []string  `json:"legal_moves"`
	History       []string  `json:"history"`     // long-algebraic, parallel to HistorySAN
	HistorySAN    []string  `json:"history_san"` // standard algebraic for display
	LastMove      *moveJSON `json:"last_move"`
	Thinking      bool      `json:"thinking"`
	TouchMove     bool      `json:"touch_move"`
	TouchedSquare string    `json:"touched_square"`
}

type moveJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type hintMove struct {
	Move  string   `json:"move"`
	From  string   `json:"from"`
	To    string   `json:"to"`
	Promo string   `json:"promo"`
	Score string   `json:"score"`
	Depth int      `json:"depth"`
	PV    []string `json:"pv"`
}

type hintResponse struct {
	Hint  *hintMove `json:"hint"`
	State stateJSON `json:"state"`
}

type assessResponse struct {
	Index     int      `json:"index"`
	Player    string   `json:"player"`
	Move      string   `json:"move"`
	BestMove  string   `json:"best_move"`
	UserScore string   `json:"user_score"`
	BestScore string   `json:"best_score"`
	CPLoss    int      `json:"cp_loss"`
	Label     string   `json:"label"`
	Depth     int      `json:"depth"`
	PV        []string `json:"pv,omitempty"`
}

type savedGame struct {
	StartFEN    string   `json:"start_fen"`
	Moves       []string `json:"moves"`
	EngineWhite bool     `json:"engine_white"`
	EngineBlack bool     `json:"engine_black"`
}

// snapshotLocked builds the state JSON. Caller must hold g.mu.
func (g *GUI) snapshotLocked() stateJSON {
	game := g.game
	legal := game.Board.GenerateLegalMoves()
	legalStrs := make([]string, len(legal))
	for i, m := range legal {
		legalStrs[i] = m.String()
	}
	turn := "w"
	if game.Board.SideToMove == Black {
		turn = "b"
	}
	var lm *moveJSON
	if game.LastMove != nil {
		lm = &moveJSON{From: SquareName(game.LastMove.From), To: SquareName(game.LastMove.To)}
	}
	hist := game.History
	if hist == nil {
		hist = []string{} // marshal as [] not null so the frontend can iterate it
	}
	histSAN := game.HistorySAN
	if histSAN == nil {
		histSAN = []string{}
	}
	touched := ""
	if game.TouchedSq != NoSquare {
		touched = SquareName(game.TouchedSq)
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
		History:       hist,
		HistorySAN:    histSAN,
		LastMove:      lm,
		Thinking:      g.thinking,
		TouchMove:     game.TouchMove,
		TouchedSquare: touched,
	}
}

// Routing -------------------------------------------------------------------

func (g *GUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mux.ServeHTTP(w, r)
}

func (g *GUI) handleReplay(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	frames := g.game.ReplayData()
	g.mu.Unlock()
	payload, _ := json.Marshal(frames)

	html := bytes.Replace(replayHTML, []byte(replayPlaceholder), payload, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="chess-replay-%s.html"`, time.Now().Format("20060102-150405")))
	}
	w.Write(html)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// Handlers ------------------------------------------------------------------

func (g *GUI) handleState(w http.ResponseWriter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleNew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EngineWhite bool `json:"engine_white"`
		EngineBlack bool `json:"engine_black"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.game.Reset()
	g.game.EngineWhite = req.EngineWhite
	g.game.EngineBlack = req.EngineBlack
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleSetPlayers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EngineWhite bool `json:"engine_white"`
		EngineBlack bool `json:"engine_black"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.thinking {
		http.Error(w, "engine is busy", 409)
		return
	}
	g.game.EngineWhite = req.EngineWhite
	g.game.EngineBlack = req.EngineBlack
	g.game.TouchedSq = NoSquare // dropped: side may now be an engine
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleTouchMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.game.TouchMove = req.Enabled
	g.game.TouchedSq = NoSquare
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleTouch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Square string `json:"square"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	sq, err := ParseSquare(req.Square)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.thinking {
		writeJSON(w, g.snapshotLocked())
		return
	}
	g.game.Touch(sq)
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Move string `json:"move"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.thinking || g.game.TouchLost || g.game.EngineToMove() {
		writeJSON(w, g.snapshotLocked())
		return
	}
	m, err := g.game.Board.ParseUCIMove(req.Move)
	if err != nil {
		writeJSON(w, g.snapshotLocked())
		return
	}
	if g.game.TouchMove && g.game.TouchedSq != NoSquare && m.From != g.game.TouchedSq {
		writeJSON(w, g.snapshotLocked())
		return
	}
	matched, ok := matchMove(g.game.Board.GenerateLegalMoves(), m)
	if !ok {
		writeJSON(w, g.snapshotLocked())
		return
	}
	g.game.PlayMove(matched)
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleUndo(w http.ResponseWriter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.thinking {
		g.game.Undo()
	}
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleEngineStep(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MoveTime int `json:"movetime"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.MoveTime <= 0 {
		req.MoveTime = 1000
	}

	g.mu.Lock()
	if g.thinking || !g.game.EngineToMove() || g.game.Status() != StatusOngoing {
		writeJSON(w, g.snapshotLocked())
		g.mu.Unlock()
		return
	}
	g.thinking = true
	board := *g.game.Board
	hist := copyHistory(g.game.posCount)
	g.mu.Unlock()

	stop := &atomic.Bool{}
	result := board.IterativeDeepening(
		SearchLimits{MoveTime: time.Duration(req.MoveTime) * time.Millisecond, History: hist},
		stop, nil,
	)

	g.mu.Lock()
	g.thinking = false
	if result.BestMove != (Move{}) {
		// Re-resolve against current legal moves in case of concurrent reset.
		if matched, ok := matchMove(g.game.Board.GenerateLegalMoves(), result.BestMove); ok {
			g.game.PlayMove(matched)
		}
	}
	snap := g.snapshotLocked()
	g.mu.Unlock()
	writeJSON(w, snap)
}

// copyHistory returns a defensive copy so the search can read it without a
// lock and without racing with subsequent PlayMove writes.
func copyHistory(src map[uint64]int) map[uint64]int {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[uint64]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (g *GUI) handleHint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MoveTime int `json:"movetime"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.MoveTime <= 0 {
		req.MoveTime = 1000
	}

	g.mu.Lock()
	if g.thinking {
		g.mu.Unlock()
		http.Error(w, "engine is busy", 409)
		return
	}
	if len(g.game.Board.GenerateLegalMoves()) == 0 {
		snap := g.snapshotLocked()
		g.mu.Unlock()
		writeJSON(w, hintResponse{State: snap})
		return
	}
	g.thinking = true
	board := *g.game.Board
	hist := copyHistory(g.game.posCount)
	g.mu.Unlock()

	stop := &atomic.Bool{}
	result := board.IterativeDeepening(
		SearchLimits{MoveTime: time.Duration(req.MoveTime) * time.Millisecond, History: hist},
		stop, nil,
	)

	g.mu.Lock()
	g.thinking = false
	snap := g.snapshotLocked()
	g.mu.Unlock()

	resp := hintResponse{State: snap}
	if result.BestMove != (Move{}) {
		m := result.BestMove
		hm := &hintMove{
			Move:  m.String(),
			From:  SquareName(m.From),
			To:    SquareName(m.To),
			Score: scoreToUCI(result.Score),
			Depth: result.Depth,
		}
		if m.Promo != Empty {
			hm.Promo = string(promoChar(m.Promo))
		}
		for _, pm := range result.PV {
			hm.PV = append(hm.PV, pm.String())
		}
		resp.Hint = hm
	}
	writeJSON(w, resp)
}

func (g *GUI) handleAssess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MoveTime int  `json:"movetime"`
		Index    *int `json:"index,omitempty"` // optional: explicit ply to grade
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.MoveTime <= 0 {
		req.MoveTime = 500
	}

	g.mu.Lock()
	if g.thinking || len(g.game.UndoStack) == 0 {
		g.mu.Unlock()
		http.Error(w, "no move to assess", 400)
		return
	}

	var targetIdx int
	if req.Index != nil {
		if *req.Index < 0 || *req.Index >= len(g.game.UndoStack) {
			g.mu.Unlock()
			http.Error(w, "index out of range", 400)
			return
		}
		targetIdx = *req.Index
	} else {
		targetIdx = g.game.LastHumanMoveIndex()
		if targetIdx < 0 {
			g.mu.Unlock()
			http.Error(w, "no human move to assess", 400)
			return
		}
	}

	g.thinking = true
	beforeBoard, afterBoard := g.game.BoardsAroundMove(targetIdx)
	userMove := g.game.UndoStack[targetIdx].Move
	playerStr := "w"
	if g.game.PlayerAt(targetIdx) == Black {
		playerStr = "b"
	}
	moveTime := time.Duration(req.MoveTime) * time.Millisecond
	g.mu.Unlock()

	stop1 := &atomic.Bool{}
	beforeRes := beforeBoard.IterativeDeepening(SearchLimits{MoveTime: moveTime}, stop1, nil)
	stop2 := &atomic.Bool{}
	afterRes := afterBoard.IterativeDeepening(SearchLimits{MoveTime: moveTime}, stop2, nil)

	g.mu.Lock()
	g.thinking = false
	g.mu.Unlock()

	bestScore := beforeRes.Score
	userScore := -afterRes.Score
	cpLoss := bestScore - userScore

	pv := make([]string, 0, len(beforeRes.PV))
	for _, pm := range beforeRes.PV {
		pv = append(pv, pm.String())
	}
	writeJSON(w, assessResponse{
		Index:     targetIdx,
		Player:    playerStr,
		Move:      userMove.String(),
		BestMove:  beforeRes.BestMove.String(),
		UserScore: scoreToUCI(userScore),
		BestScore: scoreToUCI(bestScore),
		CPLoss:    cpLoss,
		Label:     classifyAssessment(userMove, beforeRes.BestMove, cpLoss, bestScore, userScore),
		Depth:     beforeRes.Depth,
		PV:        pv,
	})
}

func (g *GUI) handleSave(w http.ResponseWriter) {
	g.mu.Lock()
	start := g.game.StartFEN
	if start == "" {
		start = StartFEN
	}
	sg := savedGame{
		StartFEN:    start, // preserves edited puzzle positions across save/load
		Moves:       append([]string(nil), g.game.History...),
		EngineWhite: g.game.EngineWhite,
		EngineBlack: g.game.EngineBlack,
	}
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="chess-%s.json"`, time.Now().Format("20060102-150405")))
	json.NewEncoder(w).Encode(sg)
}

func (g *GUI) handleLoad(w http.ResponseWriter, r *http.Request) {
	var sg savedGame
	if err := json.NewDecoder(r.Body).Decode(&sg); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), 400)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.thinking {
		http.Error(w, "engine is thinking; try again in a moment", 409)
		return
	}
	if err := g.game.Load(sg.StartFEN, sg.Moves, sg.EngineWhite, sg.EngineBlack); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, g.snapshotLocked())
}
