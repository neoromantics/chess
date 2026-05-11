package uci

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/taiyanliu/chess/pkg/core"
)

const (
	engineName   = "chessgo"
	engineAuthor = "anonymous"
)

type UCI struct {
	board *core.Board
	out   io.Writer

	mu        sync.Mutex
	stopFlag  *atomic.Bool
	searching bool
	doneCh    chan struct{}
}

func NewUCI(out io.Writer) *UCI {
	return &UCI{board: core.StartPosition(), out: out}
}

func (u *UCI) writef(format string, a ...any) {
	fmt.Fprintf(u.out, format, a...)
	fmt.Fprintln(u.out)
}

func (u *UCI) Run(in io.Reader) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "uci":
			u.writef("id name %s", engineName)
			u.writef("id author %s", engineAuthor)
			u.writef("uciok")
		case "isready":
			u.writef("readyok")
		case "ucinewgame":
			u.stopSearch()
			u.board = core.StartPosition()
		case "position":
			u.stopSearch()
			u.handlePosition(fields[1:])
		case "go":
			u.handleGo(fields[1:])
		case "stop":
			u.stopSearch()
		case "quit":
			u.stopSearch()
			return
		case "wait":
			u.waitForSearch()
		case "d", "display":
			fmt.Fprint(u.out, u.board.Display())
			u.writef("FEN: %s", u.board.FEN())
		default:
			// Unknown commands are silently ignored per UCI convention.
		}
	}
	// On EOF, drain any in-flight search so bestmove is emitted before exit.
	u.waitForSearch()
}

func (u *UCI) waitForSearch() {
	u.mu.Lock()
	done := u.doneCh
	searching := u.searching
	u.mu.Unlock()
	if searching && done != nil {
		<-done
	}
}

func (u *UCI) handlePosition(args []string) {
	if len(args) == 0 {
		return
	}
	var movesIdx int
	switch args[0] {
	case "startpos":
		u.board = core.StartPosition()
		movesIdx = 1
	case "fen":
		// FEN occupies the next up to 6 fields. Find the "moves" sentinel.
		end := len(args)
		for i := 1; i < len(args); i++ {
			if args[i] == "moves" {
				end = i
				break
			}
		}
		fen := strings.Join(args[1:end], " ")
		b, err := core.ParseFEN(fen)
		if err != nil {
			u.writef("info string bad FEN: %v", err)
			return
		}
		u.board = b
		movesIdx = end
	default:
		return
	}
	if movesIdx >= len(args) {
		return
	}
	if args[movesIdx] != "moves" {
		return
	}
	for _, ms := range args[movesIdx+1:] {
		m, err := u.board.ParseUCIMove(ms)
		if err != nil {
			u.writef("info string bad move %s: %v", ms, err)
			return
		}
		u.board.MakeMove(m)
	}
}

func (u *UCI) handleGo(args []string) {
	limits := core.SearchLimits{}
	var wtime, btime, winc, binc int
	var movestogo int
	hasClock := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "depth":
			if i+1 < len(args) {
				v, _ := strconv.Atoi(args[i+1])
				limits.MaxDepth = v
				i++
			}
		case "movetime":
			if i+1 < len(args) {
				v, _ := strconv.Atoi(args[i+1])
				limits.MoveTime = time.Duration(v) * time.Millisecond
				i++
			}
		case "wtime":
			if i+1 < len(args) {
				wtime, _ = strconv.Atoi(args[i+1])
				hasClock = true
				i++
			}
		case "btime":
			if i+1 < len(args) {
				btime, _ = strconv.Atoi(args[i+1])
				hasClock = true
				i++
			}
		case "winc":
			if i+1 < len(args) {
				winc, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "binc":
			if i+1 < len(args) {
				binc, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "movestogo":
			if i+1 < len(args) {
				movestogo, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "infinite":
			limits.Infinite = true
		}
	}

	if limits.MoveTime == 0 && hasClock && !limits.Infinite {
		myTime, myInc := wtime, winc
		if u.board.SideToMove == core.Black {
			myTime, myInc = btime, binc
		}
		var ms int
		if movestogo > 0 {
			ms = myTime/(movestogo+2) + myInc/2
		} else {
			ms = myTime/30 + myInc/2
		}
		// Leave a small safety margin and clamp.
		ms -= 50
		if ms < 10 {
			ms = 10
		}
		if ms > myTime-50 && myTime > 100 {
			ms = myTime - 50
		}
		limits.MoveTime = time.Duration(ms) * time.Millisecond
	}

	u.startSearch(limits)
}

func (u *UCI) startSearch(limits core.SearchLimits) {
	u.stopSearch()

	u.mu.Lock()
	stop := &atomic.Bool{}
	u.stopFlag = stop
	u.searching = true
	doneCh := make(chan struct{})
	u.doneCh = doneCh
	u.mu.Unlock()

	board := u.board

	go func() {
		defer close(doneCh)
		result := board.IterativeDeepening(limits, stop, func(r core.SearchResult) {
			u.printInfo(r)
		})
		// Always emit a bestmove line, even if no move was found (rare).
		bm := result.BestMove
		if bm == (core.Move{}) {
			// Fallback: pick any legal move.
			legal := board.GenerateLegalMoves()
			if len(legal) > 0 {
				bm = legal[0]
			}
		}
		if bm != (core.Move{}) {
			u.writef("bestmove %s", bm.String())
		} else {
			u.writef("bestmove 0000")
		}
		u.mu.Lock()
		u.searching = false
		u.mu.Unlock()
	}()
}

func (u *UCI) stopSearch() {
	u.mu.Lock()
	if !u.searching {
		u.mu.Unlock()
		return
	}
	stop := u.stopFlag
	done := u.doneCh
	u.mu.Unlock()
	if stop != nil {
		stop.Store(true)
	}
	if done != nil {
		<-done
	}
}

func (u *UCI) printInfo(r core.SearchResult) {
	var pvSb strings.Builder
	for i, m := range r.PV {
		if i > 0 {
			pvSb.WriteByte(' ')
		}
		pvSb.WriteString(m.String())
	}
	scoreField := ScoreToUCI(r.Score)
	nps := int64(0)
	ms := r.Elapsed.Milliseconds()
	if ms > 0 {
		nps = r.Nodes * 1000 / ms
	}
	u.writef("info depth %d score %s nodes %d nps %d time %d pv %s",
		r.Depth, scoreField, r.Nodes, nps, ms, pvSb.String())
}

func ScoreToUCI(score int) string {
	if score > core.MateInMaxPly {
		plies := core.MateScore - score
		moves := (plies + 1) / 2
		return fmt.Sprintf("mate %d", moves)
	}
	if score < -core.MateInMaxPly {
		plies := core.MateScore + score
		moves := (plies + 1) / 2
		return fmt.Sprintf("mate -%d", moves)
	}
	return fmt.Sprintf("cp %d", score)
}
