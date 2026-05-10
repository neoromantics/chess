package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed gui.html
var guiHTML []byte

type GUI struct {
	mu          sync.Mutex
	board       *Board
	startFEN    string
	history     []string
	lastMove    *Move
	engineWhite bool
	engineBlack bool
	thinking    bool

	// Touch-move (tournament-style): once a piece is "touched" it must be moved.
	// Touching a piece that has no legal moves loses the game on the spot.
	touchMove bool
	touchedSq int  // NoSquare if no piece is currently touched
	touchLost bool // sticky once a touch-move loss is declared

	// undoStack[i] is the Undo for the move at history[i]; lets /api/assess
	// roll back to the position before the user's move without replaying.
	undoStack []Undo

	// posCount tracks how many times the current line has reached each
	// position (FEN minus halfmove/fullmove fields). Cleared whenever the
	// halfmove clock resets, since older positions are no longer reachable.
	posCount map[string]int
}

// positionKey is the FEN with the halfmove and fullmove counters stripped —
// the canonical key for threefold-repetition equality.
func positionKey(b *Board) string {
	fields := strings.Fields(b.FEN())
	if len(fields) < 4 {
		return b.FEN()
	}
	return strings.Join(fields[:4], " ")
}

// insufficientMaterial reports whether neither side has enough material to
// deliver mate even with cooperative play. Recognised draws: K vs K,
// K + minor (B or N) vs K, and K + B vs K + B with same-square-color bishops.
func (g *GUI) insufficientMaterial() bool {
	var counts [2][7]int
	bishopColor := [2]int{-1, -1}
	for sq := 0; sq < 128; sq++ {
		if !OnBoard(sq) {
			continue
		}
		p := g.board.Squares[sq]
		if p.IsEmpty() {
			continue
		}
		counts[p.Color()][p.Type()]++
		if p.Type() == Bishop {
			bishopColor[p.Color()] = (FileOf(sq) + RankOf(sq)) % 2
		}
	}
	for c := 0; c < 2; c++ {
		if counts[c][Pawn] > 0 || counts[c][Rook] > 0 || counts[c][Queen] > 0 {
			return false
		}
	}
	nW := counts[White][Knight] + counts[White][Bishop]
	nB := counts[Black][Knight] + counts[Black][Bishop]
	if nW == 0 && nB == 0 {
		return true
	}
	if (nW == 1 && nB == 0) || (nB == 1 && nW == 0) {
		return true
	}
	if counts[White][Bishop] == 1 && counts[Black][Bishop] == 1 &&
		counts[White][Knight] == 0 && counts[Black][Knight] == 0 &&
		bishopColor[White] == bishopColor[Black] {
		return true
	}
	return false
}

// playMove applies a move and records it in history + undo stack. Caller
// must already hold g.mu.
func (g *GUI) playMove(m Move) {
	u := g.board.MakeMove(m)
	g.history = append(g.history, m.String())
	mv := m
	g.lastMove = &mv
	g.undoStack = append(g.undoStack, u)
	g.touchedSq = NoSquare

	// Repetition tracking. A pawn move or capture resets the halfmove clock
	// AND makes prior positions unreachable, so we flush the counter then.
	if g.board.HalfmoveClock == 0 {
		g.posCount = map[string]int{}
	}
	if g.posCount == nil {
		g.posCount = map[string]int{}
	}
	g.posCount[positionKey(g.board)]++
}

func NewGUI() *GUI {
	g := &GUI{touchedSq: NoSquare}
	g.resetLocked(false, true) // default: you play White, engine plays Black
	return g
}

func (g *GUI) resetLocked(eW, eB bool) {
	g.board = StartPosition()
	g.startFEN = StartFEN
	g.history = nil
	g.lastMove = nil
	g.engineWhite = eW
	g.engineBlack = eB
	g.thinking = false
	g.touchedSq = NoSquare
	g.touchLost = false
	g.undoStack = nil
	g.posCount = map[string]int{positionKey(g.board): 1}
	// touchMove preference is kept across resets
}

type stateJSON struct {
	FEN           string    `json:"fen"`
	Turn          string    `json:"turn"` // "w" or "b"
	EngineWhite   bool      `json:"engine_white"`
	EngineBlack   bool      `json:"engine_black"`
	EngineToMove  bool      `json:"engine_to_move"`
	Status        string    `json:"status"` // ongoing, checkmate, stalemate, draw50, touch_lost
	InCheck       bool      `json:"in_check"`
	LegalMoves    []string  `json:"legal_moves"`
	History       []string  `json:"history"`
	LastMove      *moveJSON `json:"last_move"`
	Thinking      bool      `json:"thinking"`
	TouchMove     bool      `json:"touch_move"`
	TouchedSquare string    `json:"touched_square"` // empty if none
}

type moveJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (g *GUI) snapshotLocked() stateJSON {
	legal := g.board.GenerateLegalMoves()
	legalStrs := make([]string, len(legal))
	for i, m := range legal {
		legalStrs[i] = m.String()
	}
	inCheck := g.board.InCheck(g.board.SideToMove)
	status := "ongoing"
	switch {
	case g.touchLost:
		status = "touch_lost"
	case len(legal) == 0 && inCheck:
		status = "checkmate"
	case len(legal) == 0:
		status = "stalemate"
	case g.posCount[positionKey(g.board)] >= 3:
		status = "draw_repetition"
	case g.insufficientMaterial():
		status = "draw_insufficient"
	case g.board.HalfmoveClock >= 100:
		status = "draw50"
	}
	turn := "w"
	if g.board.SideToMove == Black {
		turn = "b"
	}
	engineToMove := (turn == "w" && g.engineWhite) || (turn == "b" && g.engineBlack)
	var lm *moveJSON
	if g.lastMove != nil {
		lm = &moveJSON{From: SquareName(g.lastMove.From), To: SquareName(g.lastMove.To)}
	}
	hist := g.history
	if hist == nil {
		hist = []string{} // marshal as [] not null so the frontend can iterate it
	}
	touched := ""
	if g.touchedSq != NoSquare {
		touched = SquareName(g.touchedSq)
	}
	return stateJSON{
		FEN: g.board.FEN(), Turn: turn,
		EngineWhite: g.engineWhite, EngineBlack: g.engineBlack,
		EngineToMove: engineToMove, Status: status, InCheck: inCheck,
		LegalMoves: legalStrs, History: hist, LastMove: lm,
		Thinking:  g.thinking,
		TouchMove: g.touchMove, TouchedSquare: touched,
	}
}

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
	if g.thinking || g.touchLost {
		writeJSON(w, g.snapshotLocked())
		return
	}
	turn := g.board.SideToMove
	engineTurn := (turn == White && g.engineWhite) || (turn == Black && g.engineBlack)
	if engineTurn {
		writeJSON(w, g.snapshotLocked())
		return
	}
	// Already touched a piece — touch-move rule means you can't switch.
	if g.touchedSq != NoSquare {
		writeJSON(w, g.snapshotLocked())
		return
	}
	p := g.board.Squares[sq]
	if p.IsEmpty() || p.Color() != turn {
		writeJSON(w, g.snapshotLocked())
		return
	}
	hasMove := false
	for _, m := range g.board.GenerateLegalMoves() {
		if m.From == sq {
			hasMove = true
			break
		}
	}
	if !hasMove {
		// Touched a piece with no legal move → instant loss.
		g.touchLost = true
	} else {
		g.touchedSq = sq
	}
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
	g.engineWhite = req.EngineWhite
	g.engineBlack = req.EngineBlack
	// If the side to move just became an engine, drop any pending touch.
	g.touchedSq = NoSquare
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleTouchMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.touchMove = req.Enabled
	g.touchedSq = NoSquare // clear any pending touch when toggling
	writeJSON(w, g.snapshotLocked())
}

type assessResponse struct {
	Index     int    `json:"index"`  // 0-based ply in history
	Player    string `json:"player"` // "w" or "b"
	Move      string `json:"move"`
	BestMove  string `json:"best_move"`
	UserScore string `json:"user_score"` // from the mover's POV
	BestScore string `json:"best_score"` // from the mover's POV
	CPLoss    int    `json:"cp_loss"`    // best - user, in centipawns; can be negative
	Label     string `json:"label"`      // Best, Brilliant, Excellent, Good, Inaccuracy, Mistake, Blunder
	Depth     int    `json:"depth"`
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
	if g.thinking || len(g.undoStack) == 0 {
		g.mu.Unlock()
		http.Error(w, "no move to assess", 400)
		return
	}

	n := len(g.undoStack)
	turn := g.board.SideToMove // side to move *after* the last move
	playerAt := func(i int) Color {
		// Player of undoStack[i] = opposite of side-to-move just after move i.
		// Side-to-move just after move i = `turn` flipped (n-1-i) times.
		if (n-1-i)%2 == 0 {
			return turn.Opp()
		}
		return turn
	}

	targetIdx := -1
	if req.Index != nil {
		// Caller asked for a specific ply.
		if *req.Index < 0 || *req.Index >= n {
			g.mu.Unlock()
			http.Error(w, "index out of range", 400)
			return
		}
		targetIdx = *req.Index
	} else {
		// Default: find the last move played by a human. In human-vs-engine
		// games this skips the engine's reply so we always grade the user.
		isHuman := func(c Color) bool {
			if c == White {
				return !g.engineWhite
			}
			return !g.engineBlack
		}
		for i := n - 1; i >= 0; i-- {
			if isHuman(playerAt(i)) {
				targetIdx = i
				break
			}
		}
		if targetIdx < 0 {
			g.mu.Unlock()
			http.Error(w, "no human move to assess", 400)
			return
		}
	}

	g.thinking = true
	// Roll the live board back to just before the target move, snapshot before
	// + after positions, then replay everything so g.board is unchanged.
	for j := n - 1; j >= targetIdx; j-- {
		g.board.UnmakeMove(g.undoStack[j])
	}
	beforeBoard := *g.board
	userMove := g.undoStack[targetIdx].Move
	g.board.MakeMove(userMove)
	afterBoard := *g.board
	for j := targetIdx + 1; j < n; j++ {
		g.board.MakeMove(g.undoStack[j].Move)
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

	// Both scores in the user's (mover-of-userMove) POV. afterRes is from the
	// opponent's POV after the user's move, so negate it.
	bestScore := beforeRes.Score
	userScore := -afterRes.Score
	cpLoss := bestScore - userScore

	playerStr := "w"
	if playerAt(targetIdx) == Black {
		playerStr = "b"
	}
	resp := assessResponse{
		Index:     targetIdx,
		Player:    playerStr,
		Move:      userMove.String(),
		BestMove:  beforeRes.BestMove.String(),
		UserScore: scoreToUCI(userScore),
		BestScore: scoreToUCI(bestScore),
		CPLoss:    cpLoss,
		Label:     classifyAssessment(userMove, beforeRes.BestMove, cpLoss, bestScore, userScore),
		Depth:     beforeRes.Depth,
	}
	writeJSON(w, resp)
}

func classifyAssessment(played, best Move, cpLoss, bestScore, userScore int) string {
	// User found a deeper line than the engine — credit it.
	if cpLoss <= -50 {
		return "Brilliant"
	}
	if played.From == best.From && played.To == best.To && played.Promo == best.Promo {
		return "Best"
	}
	// Walking from non-mated into mated is always a Blunder regardless of cp math.
	if userScore <= -MateInMaxPly && bestScore > -MateInMaxPly {
		return "Blunder"
	}
	switch {
	case cpLoss <= 15:
		return "Excellent"
	case cpLoss <= 50:
		return "Good"
	case cpLoss <= 100:
		return "Inaccuracy"
	case cpLoss <= 250:
		return "Mistake"
	default:
		return "Blunder"
	}
}

type hintMove struct {
	Move  string   `json:"move"`
	From  string   `json:"from"`
	To    string   `json:"to"`
	Promo string   `json:"promo"`
	Score string   `json:"score"` // "cp 35" or "mate 3" or "mate -1"
	Depth int      `json:"depth"`
	PV    []string `json:"pv"`
}

type hintResponse struct {
	Hint  *hintMove `json:"hint"`
	State stateJSON `json:"state"`
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
	legal := g.board.GenerateLegalMoves()
	if len(legal) == 0 {
		snap := g.snapshotLocked()
		g.mu.Unlock()
		writeJSON(w, hintResponse{State: snap})
		return
	}
	g.thinking = true
	boardCopy := *g.board
	moveTime := time.Duration(req.MoveTime) * time.Millisecond
	g.mu.Unlock()

	stop := &atomic.Bool{}
	result := boardCopy.IterativeDeepening(SearchLimits{MoveTime: moveTime}, stop, nil)

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

type savedGame struct {
	StartFEN    string   `json:"start_fen"`
	Moves       []string `json:"moves"`
	EngineWhite bool     `json:"engine_white"`
	EngineBlack bool     `json:"engine_black"`
}

func (g *GUI) handleSave(w http.ResponseWriter) {
	g.mu.Lock()
	sg := savedGame{
		StartFEN:    StartFEN, // GUI always begins from the standard start position
		Moves:       append([]string(nil), g.history...),
		EngineWhite: g.engineWhite,
		EngineBlack: g.engineBlack,
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
	startFEN := sg.StartFEN
	if startFEN == "" {
		startFEN = StartFEN
	}
	b, err := ParseFEN(startFEN)
	if err != nil {
		http.Error(w, "bad start FEN: "+err.Error(), 400)
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.thinking {
		http.Error(w, "engine is thinking; try again in a moment", 409)
		return
	}
	g.board = b
	g.startFEN = startFEN
	g.history = nil
	g.lastMove = nil
	g.engineWhite = sg.EngineWhite
	g.engineBlack = sg.EngineBlack
	g.thinking = false
	g.touchedSq = NoSquare
	g.touchLost = false
	g.undoStack = nil
	g.posCount = map[string]int{positionKey(g.board): 1}

	for _, ms := range sg.Moves {
		m, err := g.board.ParseUCIMove(ms)
		if err != nil {
			http.Error(w, fmt.Sprintf("bad move %s: %v", ms, err), 400)
			return
		}
		legal := g.board.GenerateLegalMoves()
		matched, ok := matchMove(legal, m)
		if !ok {
			http.Error(w, fmt.Sprintf("illegal move %s in loaded game", ms), 400)
			return
		}
		g.playMove(matched)
	}
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleUndo(w http.ResponseWriter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.thinking || len(g.undoStack) == 0 {
		writeJSON(w, g.snapshotLocked())
		return
	}
	last := len(g.undoStack) - 1
	g.board.UnmakeMove(g.undoStack[last])
	g.history = g.history[:last]
	g.undoStack = g.undoStack[:last]
	g.touchedSq = NoSquare
	g.touchLost = false
	if last > 0 {
		mv := g.undoStack[last-1].Move
		g.lastMove = &mv
	} else {
		g.lastMove = nil
	}
	// Rebuild posCount by replaying from startFEN.
	b, _ := ParseFEN(g.startFEN)
	g.posCount = map[string]int{positionKey(b): 1}
	for _, u := range g.undoStack {
		b.MakeMove(u.Move)
		if b.HalfmoveClock == 0 {
			g.posCount = map[string]int{}
		}
		g.posCount[positionKey(b)]++
	}
	writeJSON(w, g.snapshotLocked())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (g *GUI) handleState(w http.ResponseWriter) {
	g.mu.Lock()
	snap := g.snapshotLocked()
	g.mu.Unlock()
	writeJSON(w, snap)
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
	if g.thinking || g.touchLost {
		writeJSON(w, g.snapshotLocked())
		return
	}
	turn := g.board.SideToMove
	engineTurn := (turn == White && g.engineWhite) || (turn == Black && g.engineBlack)
	if engineTurn {
		writeJSON(w, g.snapshotLocked())
		return
	}
	m, err := g.board.ParseUCIMove(req.Move)
	if err != nil {
		writeJSON(w, g.snapshotLocked())
		return
	}
	// Touch-move enforcement: if a piece has been touched, the move must use it.
	if g.touchMove && g.touchedSq != NoSquare && m.From != g.touchedSq {
		writeJSON(w, g.snapshotLocked())
		return
	}
	legal := g.board.GenerateLegalMoves()
	matched, ok := matchMove(legal, m)
	if !ok {
		writeJSON(w, g.snapshotLocked())
		return
	}
	g.playMove(matched)
	writeJSON(w, g.snapshotLocked())
}

func matchMove(legal []Move, m Move) (Move, bool) {
	for _, lm := range legal {
		if lm.From == m.From && lm.To == m.To && lm.Promo == m.Promo {
			return lm, true
		}
	}
	return Move{}, false
}

func (g *GUI) handleNew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EngineWhite bool `json:"engine_white"`
		EngineBlack bool `json:"engine_black"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	g.mu.Lock()
	g.resetLocked(req.EngineWhite, req.EngineBlack)
	snap := g.snapshotLocked()
	g.mu.Unlock()
	writeJSON(w, snap)
}

func (g *GUI) handleEngineStep(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MoveTime int `json:"movetime"` // ms
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.MoveTime <= 0 {
		req.MoveTime = 1000
	}

	g.mu.Lock()
	if g.thinking {
		snap := g.snapshotLocked()
		g.mu.Unlock()
		writeJSON(w, snap)
		return
	}
	turn := g.board.SideToMove
	engineTurn := (turn == White && g.engineWhite) || (turn == Black && g.engineBlack)
	legal := g.board.GenerateLegalMoves()
	if !engineTurn || len(legal) == 0 {
		snap := g.snapshotLocked()
		g.mu.Unlock()
		writeJSON(w, snap)
		return
	}
	g.thinking = true
	// Search a copy so /api/state can read g.board concurrently.
	boardCopy := *g.board
	moveTime := time.Duration(req.MoveTime) * time.Millisecond
	g.mu.Unlock()

	stop := &atomic.Bool{}
	result := boardCopy.IterativeDeepening(SearchLimits{MoveTime: moveTime}, stop, nil)

	g.mu.Lock()
	g.thinking = false
	if result.BestMove != (Move{}) {
		// Re-resolve against current legal moves in case of concurrent reset.
		liveLegal := g.board.GenerateLegalMoves()
		if matched, ok := matchMove(liveLegal, result.BestMove); ok {
			g.playMove(matched)
		}
	}
	snap := g.snapshotLocked()
	g.mu.Unlock()
	writeJSON(w, snap)
}
