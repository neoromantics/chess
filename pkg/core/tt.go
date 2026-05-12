package core

// Transposition table: a fixed-size, always-replace direct-mapped cache
// of search results keyed by Zobrist hash. Lets the engine reuse work
// across positions reached by transposition (different move orders
// arriving at the same board) and across deepening iterations.

type ttFlag uint8

const (
	ttExact      ttFlag = iota // score is exact (PV node, alpha < score < beta)
	ttLowerBound               // score is a lower bound (beta cutoff: actual ≥ score)
	ttUpperBound               // score is an upper bound (no move beat alpha)
)

type ttEntry struct {
	Key   uint64
	Move  Move
	Score int16
	Depth int8
	Flag  ttFlag
}

type TT struct {
	entries []ttEntry
}

// NewTT allocates a table whose total memory is about sizeMB megabytes.
func NewTT(sizeMB int) *TT {
	const entryBytes = 24 // approx; padding-tolerant
	n := sizeMB * 1024 * 1024 / entryBytes
	if n < 1024 {
		n = 1024
	}
	return &TT{entries: make([]ttEntry, n)}
}

func (t *TT) index(key uint64) int { return int(key % uint64(len(t.entries))) }

// Probe looks up the entry for key. The bool indicates a key match;
// callers still need to check Depth before using Score for cutoffs.
func (t *TT) Probe(key uint64) (ttEntry, bool) {
	e := t.entries[t.index(key)]
	if e.Key == key {
		return e, true
	}
	return ttEntry{}, false
}

// Store overwrites whatever's at this slot (always-replace policy).
// Mate scores are converted to "distance from this node" so the entry
// is reusable from any depth.
func (t *TT) Store(key uint64, move Move, score, depth, ply int, flag ttFlag) {
	if score >= MateInMaxPly {
		score += ply
	} else if score <= -MateInMaxPly {
		score -= ply
	}
	t.entries[t.index(key)] = ttEntry{
		Key:   key,
		Move:  move,
		Score: int16(score),
		Depth: int8(depth),
		Flag:  flag,
	}
}

// scoreFromTT reverses the mate-distance compression so the score is
// expressed relative to the search root rather than the storing node.
func scoreFromTT(score int16, ply int) int {
	s := int(score)
	if s >= MateInMaxPly {
		return s - ply
	}
	if s <= -MateInMaxPly {
		return s + ply
	}
	return s
}

// ExtractPV retrieves the Principal Variation from the TT.
func (t *TT) ExtractPV(b *Board, depth int) []Move {
	var pv []Move
	visited := make(map[uint64]bool)
	tempB := *b

	for i := 0; i < depth; i++ {
		if visited[tempB.Hash] {
			break
		}
		visited[tempB.Hash] = true

		entry, ok := t.Probe(tempB.Hash)
		if !ok || entry.Move == (Move{}) {
			break
		}

		// Verify legal
		legal := tempB.GenerateLegalMoves()
		found := false
		for _, m := range legal {
			if m.Equal(entry.Move) {
				found = true
				break
			}
		}
		if !found {
			break
		}

		pv = append(pv, entry.Move)
		tempB.MakeMove(entry.Move)
	}
	return pv
}
