package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed gui.html
var guiHTML []byte

// GUI is a thin HTTP shim over Game. It owns concurrency (mu) and search
// lifecycle (thinking flag); all chess logic lives in Game.
type GUI struct {
	mu       sync.Mutex
	game     *Game
	thinking bool
}

func NewGUI() *GUI {
	g := &GUI{game: NewGame()}
	g.game.EngineBlack = true // default: human plays White, engine plays Black
	return g
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
	History       []string  `json:"history"`
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
		LastMove:      lm,
		Thinking:      g.thinking,
		TouchMove:     game.TouchMove,
		TouchedSquare: touched,
	}
}

// Routing -------------------------------------------------------------------

func (g *GUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "GET" && r.URL.Path == "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(guiHTML)
	case r.Method == "GET" && r.URL.Path == "/api/state":
		g.handleState(w)
	case r.Method == "POST" && r.URL.Path == "/api/move":
		g.handleMove(w, r)
	case r.Method == "POST" && r.URL.Path == "/api/new":
		g.handleNew(w, r)
	case r.Method == "POST" && r.URL.Path == "/api/engine_step":
		g.handleEngineStep(w, r)
	case r.Method == "POST" && r.URL.Path == "/api/hint":
		g.handleHint(w, r)
	case r.Method == "POST" && r.URL.Path == "/api/touch":
		g.handleTouch(w, r)
	case r.Method == "POST" && r.URL.Path == "/api/touch_move":
		g.handleTouchMove(w, r)
	case r.Method == "POST" && r.URL.Path == "/api/assess":
		g.handleAssess(w, r)
	case r.Method == "POST" && r.URL.Path == "/api/set_players":
		g.handleSetPlayers(w, r)
	case r.Method == "GET" && r.URL.Path == "/api/save":
		g.handleSave(w)
	case r.Method == "POST" && r.URL.Path == "/api/load":
		g.handleLoad(w, r)
	case r.Method == "POST" && r.URL.Path == "/api/undo":
		g.handleUndo(w)
	default:
		http.NotFound(w, r)
	}
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

// beginSearch acquires thinking, snapshots the live board for off-mutex
// search, and returns a finish callback that re-acquires the lock and
// runs `apply` against the live game. Returns false if the engine is
// already busy or there's nothing to do.
func (g *GUI) beginSearch(movetime time.Duration) (board Board, finish func(apply func()), ok bool) {
	g.mu.Lock()
	if g.thinking {
		g.mu.Unlock()
		return Board{}, nil, false
	}
	g.thinking = true
	board = *g.game.Board
	g.mu.Unlock()

	finish = func(apply func()) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.thinking = false
		if apply != nil {
			apply()
		}
	}
	return board, finish, true
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
func copyHistory(src map[string]int) map[string]int {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]int, len(src))
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
	// Roll the live board back to just before the target move, snapshot
	// before + after positions, then replay everything so g.game.Board is
	// unchanged.
	for j := len(g.game.UndoStack) - 1; j >= targetIdx; j-- {
		g.game.Board.UnmakeMove(g.game.UndoStack[j])
	}
	beforeBoard := *g.game.Board
	userMove := g.game.UndoStack[targetIdx].Move
	g.game.Board.MakeMove(userMove)
	afterBoard := *g.game.Board
	for j := targetIdx + 1; j < len(g.game.UndoStack); j++ {
		g.game.Board.MakeMove(g.game.UndoStack[j].Move)
	}
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
