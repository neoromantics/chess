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

	"github.com/taiyanliu/chess/pkg/core"
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
	// Pre-load HTML files for the handlers to use as templates.
	var err error
	guiHTML, err = frontendDist.ReadFile("dist/index.html")
	if err != nil {
		panic(fmt.Sprintf("failed to read index.html: %v", err))
	}
	replayHTML, err = frontendDist.ReadFile("dist/replay.html")
	if err != nil {
		panic(fmt.Sprintf("failed to read replay.html: %v", err))
	}

	// Create a sub-FS for the assets directory.
	sub, err := fs.Sub(frontendDist, "dist")
	if err != nil {
		panic(err)
	}
	assetsFS = http.FS(sub)
}

const replayPlaceholder = "REPLAY_DATA_PLACEHOLDER"

// GUI is a thin HTTP shim over Game. It owns concurrency (mu) and search
// lifecycle (thinking flag); all chess logic lives in Game.
type GUI struct {
	mux  *http.ServeMux
	mu   sync.Mutex
	game *game.Game

	thinking atomic.Bool
	lastPing time.Time
}

func NewGUI() *GUI {
	g := &GUI{game: game.NewGame(), lastPing: time.Now()}
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
				fmt.Fprintf(os.Stderr, "idle for %v, exiting\n", d)
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
	g.mux.Handle("GET /assets/", http.FileServer(assetsFS))
	g.mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { g.handleState(w) })
	g.mux.HandleFunc("POST /api/move", g.handleMove)
	g.mux.HandleFunc("POST /api/new", g.handleNew)
	g.mux.HandleFunc("POST /api/engine_step", g.handleEngineStep)
	g.mux.HandleFunc("POST /api/hint", g.handleHint)
	g.mux.HandleFunc("POST /api/assess", g.handleAssess)
	g.mux.HandleFunc("POST /api/set_players", g.handleSetPlayers)
	g.mux.HandleFunc("POST /api/touch", g.handleTouch)
	g.mux.HandleFunc("POST /api/touch_move", g.handleTouchMove)
	g.mux.HandleFunc("POST /api/undo", func(w http.ResponseWriter, r *http.Request) { g.handleUndo(w) })
	g.mux.HandleFunc("POST /api/ping", g.handlePing)
	g.mux.HandleFunc("GET /api/save", func(w http.ResponseWriter, r *http.Request) { g.handleSave(w) })
	g.mux.HandleFunc("POST /api/load", g.handleLoad)
	g.mux.HandleFunc("GET /api/replay.html", g.handleReplay)
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

func (g *GUI) snapshotLocked() stateJSON {
	game := g.game
	var lm *moveJSON
	if game.LastMove != nil {
		lm = &moveJSON{From: core.SquareName(game.LastMove.From), To: core.SquareName(game.LastMove.To)}
	}
	legalStrs := []string{}
	for _, m := range game.Board.GenerateLegalMoves() {
		legalStrs = append(legalStrs, m.String())
	}
	turn := "w"
	if game.Board.SideToMove == core.Black {
		turn = "b"
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
		History:       append([]string(nil), game.History...),
		HistorySAN:    append([]string(nil), game.HistorySAN...),
		LastMove:      lm,
		Thinking:      g.thinking.Load(),
		TouchMove:     game.TouchMove,
		TouchedSquare: core.SquareName(game.TouchedSq),
	}
}

// Handlers ------------------------------------------------------------------

func (g *GUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS headers for development/remote web UI
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")

	if r.Method == "OPTIONS" {
		return
	}

	g.mux.ServeHTTP(w, r)
}

func (g *GUI) handleReplay(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	data, _ := json.Marshal(g.game.ReplayData())
	html := bytes.Replace(replayHTML, []byte(replayPlaceholder), data, 1)
	w.Header().Set("Content-Type", "text/html")
	w.Write(html)
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

func (g *GUI) handleState(w http.ResponseWriter) {
	g.mu.Lock()
	defer g.mu.Unlock()
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
	sq, err := core.ParseSquare(req.Square)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.game.Touch(sq)
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleTouchMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.game.TouchMove = req.Enabled
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
	if g.thinking.Load() || g.game.TouchLost || g.game.EngineToMove() {
		http.Error(w, "not your turn or busy", 409)
		return
	}
	m, err := g.game.Board.ParseUCIMove(req.Move)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if g.game.TouchMove && g.game.TouchedSq != core.NoSquare && m.From != g.game.TouchedSq {
		http.Error(w, "touch-move violation", 403)
		return
	}
	matched, ok := game.MatchMove(g.game.Board.GenerateLegalMoves(), m)
	if !ok {
		http.Error(w, "illegal move", 403)
		return
	}
	g.game.PlayMove(matched)
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleNew(w http.ResponseWriter, r *http.Request) {
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
	if g.thinking.Load() {
		http.Error(w, "busy", 409)
		return
	}
	g.game.Reset()
	g.game.EngineWhite = req.EngineWhite
	g.game.EngineBlack = req.EngineBlack
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleEngineStep(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MoveTime int `json:"movetime"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.MoveTime <= 0 {
		req.MoveTime = 1000
	}

	g.mu.Lock()
	if g.thinking.Load() || !g.game.EngineToMove() || g.game.Status() != game.StatusOngoing {
		g.mu.Unlock()
		http.Error(w, "busy or not engine turn", 409)
		return
	}
	board := *g.game.Board
	hist := game.CopyHistory(g.game.HistoryHash())
	g.thinking.Store(true)
	g.mu.Unlock()

	defer g.thinking.Store(false)

	stop := &atomic.Bool{}
	result := board.IterativeDeepening(
		core.SearchLimits{MoveTime: time.Duration(req.MoveTime) * time.Millisecond, History: hist},
		stop,
		nil,
	)

	g.mu.Lock()
	defer g.mu.Unlock()
	if result.BestMove != (core.Move{}) {
		if matched, ok := game.MatchMove(g.game.Board.GenerateLegalMoves(), result.BestMove); ok {
			g.game.PlayMove(matched)
		}
	}
	writeJSON(w, g.snapshotLocked())
}

// CopyHistory helps pass game repetition state to the engine without holding the
// lock and without racing with subsequent PlayMove writes.
func CopyHistory(src map[uint64]int) map[uint64]int {
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.MoveTime <= 0 {
		req.MoveTime = 1000
	}

	g.mu.Lock()
	if g.thinking.Load() {
		g.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	if len(g.game.Board.GenerateLegalMoves()) == 0 {
		g.mu.Unlock()
		writeJSON(w, map[string]any{"state": g.snapshotLocked()})
		return
	}
	board := *g.game.Board
	hist := game.CopyHistory(g.game.HistoryHash())
	g.thinking.Store(true)
	g.mu.Unlock()

	defer g.thinking.Store(false)

	stop := &atomic.Bool{}
	result := board.IterativeDeepening(
		core.SearchLimits{MoveTime: time.Duration(req.MoveTime) * time.Millisecond, History: hist},
		stop,
		nil,
	)

	type hintMove struct {
		Move  string   `json:"move"`
		From  string   `json:"from"`
		To    string   `json:"to"`
		Promo string   `json:"promo,omitempty"`
		Score string   `json:"score"`
		Depth int      `json:"depth"`
		PV    []string `json:"pv"`
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	res := map[string]any{"state": g.snapshotLocked()}
	if result.BestMove != (core.Move{}) {
		m := result.BestMove
		hm := &hintMove{
			Move:  m.String(),
			From:  core.SquareName(m.From),
			To:    core.SquareName(m.To),
			Promo: string(core.PromoChar(m.Promo)),
			Score: uci.ScoreToUCI(result.Score),
			Depth: result.Depth,
			PV:    result.PVStrings(),
		}
		res["hint"] = hm
	}
	writeJSON(w, res)
}

func (g *GUI) handleAssess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MoveTime int  `json:"movetime"`
		Index    *int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.MoveTime <= 0 {
		req.MoveTime = 500
	}

	g.mu.Lock()
	if g.thinking.Load() {
		g.mu.Unlock()
		http.Error(w, "busy", 409)
		return
	}
	targetIdx := -1
	if req.Index != nil {
		targetIdx = *req.Index
	} else {
		targetIdx = g.game.LastHumanMoveIndex()
	}
	if targetIdx < 0 || targetIdx >= len(g.game.UndoStack) {
		g.mu.Unlock()
		http.Error(w, "no move to assess", 400)
		return
	}

	beforeBoard, afterBoard := g.game.BoardsAroundMove(targetIdx)
	userMove := g.game.UndoStack[targetIdx].Move
	player := g.game.PlayerAt(targetIdx)
	g.thinking.Store(true)
	g.mu.Unlock()

	defer g.thinking.Store(false)

	moveTime := time.Duration(req.MoveTime) * time.Millisecond
	stop1, stop2 := &atomic.Bool{}, &atomic.Bool{}
	beforeRes := beforeBoard.IterativeDeepening(core.SearchLimits{MoveTime: moveTime}, stop1, nil)
	afterRes := afterBoard.IterativeDeepening(core.SearchLimits{MoveTime: moveTime}, stop2, nil)

	bestScore := beforeRes.Score
	userScore := afterRes.Score
	if player == core.Black {
		bestScore = -bestScore
		userScore = -userScore
	}
	cpLoss := bestScore - userScore

	writeJSON(w, map[string]any{
		"index":      targetIdx,
		"player":     player.String(),
		"move":       userMove.String(),
		"best_move":  beforeRes.BestMove.String(),
		"user_score": uci.ScoreToUCI(beforeRes.Score), // beforeRes.Score is side-to-move relative
		"best_score": uci.ScoreToUCI(afterRes.Score),
		"cp_loss":    cpLoss,
		"label":      game.ClassifyAssessment(userMove, beforeRes.BestMove, cpLoss, bestScore, userScore),
		"depth":      beforeRes.Depth,
	})
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
	g.game.EngineWhite = req.EngineWhite
	g.game.EngineBlack = req.EngineBlack
	writeJSON(w, g.snapshotLocked())
}

func (g *GUI) handleUndo(w http.ResponseWriter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.thinking.Load() {
		http.Error(w, "busy", 409)
		return
	}
	g.game.Undo()
	writeJSON(w, g.snapshotLocked())
}

type savedGame struct {
	StartFEN    string   `json:"start_fen"`
	Moves       []string `json:"moves"`
	EngineWhite bool     `json:"engine_white"`
	EngineBlack bool     `json:"engine_black"`
}

func (g *GUI) handleSave(w http.ResponseWriter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	sg := savedGame{
		StartFEN:    g.game.StartFEN,
		Moves:       append([]string(nil), g.game.History...),
		EngineWhite: g.game.EngineWhite,
		EngineBlack: g.game.EngineBlack,
	}
	w.Header().Set("Content-Disposition", "attachment; filename=chess-game.json")
	writeJSON(w, sg)
}

func (g *GUI) handleLoad(w http.ResponseWriter, r *http.Request) {
	var sg savedGame
	if err := json.NewDecoder(r.Body).Decode(&sg); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.thinking.Load() {
		http.Error(w, "engine is thinking; try again in a moment", 409)
		return
	}
	if err := g.game.Load(sg.StartFEN, sg.Moves, sg.EngineWhite, sg.EngineBlack); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, g.snapshotLocked())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
