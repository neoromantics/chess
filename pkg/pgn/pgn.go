// Package pgn encodes and decodes Portable Game Notation. PGN is the
// chess-world's standard interchange format — chess.com, lichess, and
// every desktop analysis tool eat it. We use it for the Save / Load
// feature on engine games.
//
// Scope: we implement enough of the spec to round-trip our own games
// and to import games from the major sites. Specifically:
//   - Seven Tag Roster (Event, Site, Date, Round, White, Black, Result).
//   - Optional [SetUp "1"] + [FEN "..."] for non-standard start positions.
//   - SAN movetext separated by whitespace, with move numbers and the
//     result token as the final element.
//   - "{...}" comments and "(... )" variations are stripped on decode.
//     We don't preserve them on re-encode — the engine doesn't use
//     them and round-trip fidelity isn't a goal.
package pgn

import (
	"fmt"
	"strings"
	"time"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/game"
)

// Headers is the metadata block at the top of a PGN file. Anything the
// caller doesn't set falls back to a "-" placeholder, per spec.
type Headers struct {
	Event  string
	Site   string
	Date   string // "YYYY.MM.DD" — call FormatDate to format a time.Time
	Round  string
	White  string
	Black  string
	Result string // "1-0", "0-1", "1/2-1/2", "*"
}

// FormatDate returns "YYYY.MM.DD" or "????.??.??" for the zero time.
// PGN dates are local-calendar with no timezone; we emit UTC so file
// names sort the same on every viewer.
func FormatDate(t time.Time) string {
	if t.IsZero() {
		return "????.??.??"
	}
	return t.UTC().Format("2006.01.02")
}

// Encode renders a game as PGN text. startFEN is the position the moves
// were played from ("" = standard start); historySAN is the move list
// in standard algebraic notation. result is the final result token
// ("*" if the game is still in progress).
func Encode(h Headers, startFEN string, historySAN []string, result string) string {
	defaults := func(s, fallback string) string {
		if strings.TrimSpace(s) == "" {
			return fallback
		}
		return s
	}
	if strings.TrimSpace(result) == "" {
		result = "*"
	}
	rs := defaults(h.Result, result)

	var sb strings.Builder
	tag := func(k, v string) {
		fmt.Fprintf(&sb, "[%s %q]\n", k, v)
	}
	tag("Event", defaults(h.Event, "Casual Game"))
	tag("Site", defaults(h.Site, "?"))
	tag("Date", defaults(h.Date, "????.??.??"))
	tag("Round", defaults(h.Round, "-"))
	tag("White", defaults(h.White, "?"))
	tag("Black", defaults(h.Black, "?"))
	tag("Result", rs)
	// [FEN]/[SetUp] only when we're not at the canonical start. Viewers
	// that don't honor [FEN] will replay nonsense moves; emitting the
	// header is the standard fix.
	if startFEN != "" && startFEN != core.StartFEN {
		tag("SetUp", "1")
		tag("FEN", startFEN)
	}
	sb.WriteByte('\n')

	// Move text: number white-ply black-ply, wrapped at ~80 cols so the
	// output looks like every other tool emits.
	startMoveNum, startIsBlack := startMoveNumber(startFEN)
	moveNum := startMoveNum
	col := 0
	writeToken := func(tok string) {
		if col > 0 && col+1+len(tok) > 80 {
			sb.WriteByte('\n')
			col = 0
		}
		if col > 0 {
			sb.WriteByte(' ')
			col++
		}
		sb.WriteString(tok)
		col += len(tok)
	}
	for i, san := range historySAN {
		// Half-move 0 of a position where black is to move (e.g. FEN
		// starting at black's turn) needs "N..." continuation syntax.
		whiteSide := !startIsBlack
		if (whiteSide && i%2 == 0) || (!whiteSide && i%2 == 1) {
			writeToken(fmt.Sprintf("%d.", moveNum))
		} else if i == 0 && startIsBlack {
			writeToken(fmt.Sprintf("%d...", moveNum))
		}
		writeToken(san)
		// Increment after black's ply (or after white's ply when we
		// started on black).
		if (whiteSide && i%2 == 1) || (!whiteSide && i%2 == 0) {
			moveNum++
		}
	}
	writeToken(rs)
	sb.WriteByte('\n')
	return sb.String()
}

// startMoveNumber pulls the fullmove counter + side-to-move out of a
// FEN. Defaults to (1, false) — move 1, white-to-move — for the empty
// string or a malformed FEN. We never error here because we'd rather
// emit a slightly-wrong move-number column than refuse to download
// the game.
func startMoveNumber(fen string) (n int, blackToMove bool) {
	if fen == "" {
		return 1, false
	}
	parts := strings.Fields(fen)
	if len(parts) < 6 {
		return 1, false
	}
	var mn int
	if _, err := fmt.Sscanf(parts[5], "%d", &mn); err != nil || mn < 1 {
		mn = 1
	}
	return mn, parts[1] == "b"
}

// Decoded is what Decode returns. UCIMoves is the validated, replayable
// move list in long-algebraic notation (same shape we store in
// GameRecord.History); the caller can stuff it straight into
// game.Game.Load.
type Decoded struct {
	Headers  map[string]string
	StartFEN string   // "" if no [FEN] tag (= standard start)
	UCIMoves []string // validated against legal replay from StartFEN
	Result   string   // result token from the movetext (may differ from
	// Headers["Result"]; the movetext token wins per
	// PGN convention)
}

// Decode parses PGN text. It accepts a single game; multi-game files
// (PGN allows several games in one file separated by blank lines) yield
// just the first. Move validation replays every ply through the engine
// — an illegal move aborts decoding with an explanatory error so the
// caller can report which token failed.
func Decode(s string) (*Decoded, error) {
	headers, body := splitHeaders(s)
	movetext := stripCommentsAndVariations(body)

	startFEN := headers["FEN"] // "" if absent
	if startFEN != "" {
		if _, err := core.ParseFEN(startFEN); err != nil {
			return nil, fmt.Errorf("invalid FEN header: %w", err)
		}
	}

	tokens := strings.Fields(movetext)
	gm := game.NewGame()
	if err := gm.Load(startFEN, nil, false, false); err != nil {
		return nil, fmt.Errorf("load start position: %w", err)
	}

	var uci []string
	var result string
	for _, tok := range tokens {
		// Strip NAGs (e.g. "$1") and annotation glyphs.
		if strings.HasPrefix(tok, "$") {
			continue
		}
		// Move-number tokens: "1.", "1...", "10.", "10...". May appear
		// either standalone OR glued to the next SAN ("1...e5"); strip
		// the prefix in either case.
		if isMoveNumber(tok) {
			continue
		}
		tok = stripMoveNumberPrefix(tok)
		if tok == "" {
			continue
		}
		// Result tokens may appear at the end OR (in malformed exports)
		// embedded mid-list. Once we see one, treat anything after as
		// trailing garbage.
		if tok == "1-0" || tok == "0-1" || tok == "1/2-1/2" || tok == "*" {
			result = tok
			break
		}
		// Strip annotation suffixes (!, ?, !!, etc.) — they're not part
		// of the move and confuse our SAN matcher.
		san := stripAnnotations(tok)
		if san == "" {
			continue
		}
		mv, err := resolveSAN(gm.Board, san)
		if err != nil {
			return nil, fmt.Errorf("move %d (%q): %w", len(uci)+1, tok, err)
		}
		uci = append(uci, mv.String())
		gm.PlayMove(mv)
	}
	if result == "" {
		result = headers["Result"]
	}
	if result == "" {
		result = "*"
	}
	return &Decoded{
		Headers:  headers,
		StartFEN: startFEN,
		UCIMoves: uci,
		Result:   result,
	}, nil
}

// splitHeaders pulls the [Tag "Value"] block off the top of a PGN. The
// PGN spec says headers must precede movetext separated by at least one
// blank line; in practice tools omit the blank line, so we tolerate
// the boundary being the first non-tag line.
func splitHeaders(s string) (map[string]string, string) {
	h := map[string]string{}
	lines := strings.Split(s, "\n")
	i := 0
	for ; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "[") {
			break
		}
		// Tag pair: [Name "Value"]. Quoting may contain escaped quotes
		// (\\\") — the loose split here handles the common case.
		end := strings.LastIndex(line, "]")
		if end < 0 {
			break
		}
		body := strings.TrimSpace(line[1:end])
		sp := strings.Index(body, " ")
		if sp < 0 {
			continue
		}
		name := body[:sp]
		value := strings.TrimSpace(body[sp+1:])
		value = strings.TrimPrefix(value, `"`)
		value = strings.TrimSuffix(value, `"`)
		h[name] = value
	}
	return h, strings.Join(lines[i:], "\n")
}

// stripCommentsAndVariations removes "{...}" comments and "(...)"
// recursive variations from the move text. We don't replay them.
// Nesting is rare but handled with depth counters.
func stripCommentsAndVariations(s string) string {
	var sb strings.Builder
	braceDepth, parenDepth := 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '{':
			braceDepth++
		case c == '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case c == '(' && braceDepth == 0:
			parenDepth++
		case c == ')' && braceDepth == 0:
			if parenDepth > 0 {
				parenDepth--
			}
		case braceDepth == 0 && parenDepth == 0:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func isMoveNumber(tok string) bool {
	// Accept "N.", "N...", or bare "N" followed by trailing dots.
	if tok == "" {
		return false
	}
	i := 0
	for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	for i < len(tok) && tok[i] == '.' {
		i++
	}
	return i == len(tok)
}

// stripMoveNumberPrefix peels "N." / "N..." off the front of a token
// like "1...e5" → "e5". Some exporters omit the space between number
// and SAN after a comment or variation.
func stripMoveNumberPrefix(tok string) string {
	i := 0
	for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
		i++
	}
	if i == 0 {
		return tok
	}
	j := i
	for j < len(tok) && tok[j] == '.' {
		j++
	}
	if j == i {
		// Digits with no trailing dot — not a move-number prefix.
		return tok
	}
	return tok[j:]
}

func stripAnnotations(tok string) string {
	// Trim trailing !, ?, !!, ??, !?, ?!, +, # — the legality search
	// will re-derive check/mate suffixes when round-tripping.
	end := len(tok)
	for end > 0 {
		c := tok[end-1]
		if c == '!' || c == '?' || c == '+' || c == '#' {
			end--
			continue
		}
		break
	}
	return tok[:end]
}

// resolveSAN finds the legal move on b whose SAN representation
// matches san. Disambiguation, captures, promotions, castling all
// route through the same comparison.
func resolveSAN(b *core.Board, san string) (core.Move, error) {
	legal := b.GenerateLegalMoves()
	// Strip the check/mate suffix from candidate SANs too — many
	// importers emit raw "Nf3" without "+" while our generator does.
	target := strings.TrimRight(san, "+#")
	for _, m := range legal {
		cand := strings.TrimRight(core.MoveToSAN(b, m), "+#")
		if cand == target {
			return m, nil
		}
	}
	return core.Move{}, fmt.Errorf("no legal move matches SAN %q", san)
}
