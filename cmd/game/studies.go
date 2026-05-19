package main

// Studies surface: saved positions + exploration trees, persistent per
// user. The same row shape handles two intents — "save a setup" (an
// empty tree with just a starting FEN, used to bank a puzzle position
// or a position-to-study) and "save a session" (a tree of moves the
// user explored from that starting FEN). All endpoints scoped by the
// gateway-injected X-User-ID; non-owner reads return 404 not 403 to
// preserve the existence-leak rule the rest of the game surface uses.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
)

// studyTreeNode is the recursive shape stored in the tree JSONB column.
// Root node has no move; descendants carry the move that produced their
// position. san/comment are optional metadata. Validation only checks
// "looks roughly right" — we don't replay the moves against pkg/core
// because (a) that's a much bigger commitment for a save-then-view
// surface, (b) editor-driven setups may not have legal moves at all,
// and (c) corrupt trees are bounded blast radius (one user's one row).
type studyTreeNode struct {
	Move     string          `json:"move,omitempty"`
	SAN      string          `json:"san,omitempty"`
	Comment  string          `json:"comment,omitempty"`
	Children []studyTreeNode `json:"children"`
}

const (
	studyMaxNameBytes = 200
	studyMaxFENBytes  = 200
	studyMaxTreeBytes = 256 * 1024 // 256 KB per study — generous; saves on the order of 50K-node trees
)

// validateStudyTree parses + lightly validates the tree JSON. Returns
// the bytes ready to insert (re-marshalled so we drop unknown fields)
// and an error suitable for surfacing to the caller. Empty-children
// is allowed at every level — that's a leaf.
func validateStudyTree(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{"children": []}`), nil
	}
	if len(raw) > studyMaxTreeBytes {
		return nil, errors.New("tree too large")
	}
	var root studyTreeNode
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	if root.Move != "" || root.SAN != "" {
		return nil, errors.New("root node must not carry a move")
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return out, nil
}

type createStudyRequest struct {
	Name         string          `json:"name"`
	StartFEN     string          `json:"start_fen"`
	Tree         json.RawMessage `json:"tree,omitempty"`
	SourceGameID string          `json:"source_game_id,omitempty"`
	SourcePly    int             `json:"source_ply,omitempty"`
}

type updateStudyRequest struct {
	Name string          `json:"name"`
	Tree json.RawMessage `json:"tree"`
}

func (s *GameService) handleCreateStudy(w http.ResponseWriter, r *http.Request) {
	uid, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req createStudyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.StartFEN = strings.TrimSpace(req.StartFEN)
	if req.Name == "" || len(req.Name) > studyMaxNameBytes {
		http.Error(w, "name required (1.."+strconv.Itoa(studyMaxNameBytes)+" chars)", http.StatusBadRequest)
		return
	}
	if req.StartFEN == "" || len(req.StartFEN) > studyMaxFENBytes {
		http.Error(w, "start_fen required (1.."+strconv.Itoa(studyMaxFENBytes)+" chars)", http.StatusBadRequest)
		return
	}
	tree, err := validateStudyTree(req.Tree)
	if err != nil {
		http.Error(w, "invalid tree: "+err.Error(), http.StatusBadRequest)
		return
	}
	st := &db.Study{
		UserID:       uid,
		Name:         req.Name,
		StartFEN:     req.StartFEN,
		Tree:         tree,
		SourceGameID: strings.TrimSpace(req.SourceGameID),
		SourcePly:    req.SourcePly,
	}
	saved, err := s.db.CreateStudy(st)
	if err != nil {
		slog.Error("create study failed", "user_id", uid, "error", err)
		http.Error(w, "failed to create study", http.StatusInternalServerError)
		return
	}
	slog.Info("study created", "id", saved.ID, "user_id", uid, "source_game_id", saved.SourceGameID)
	writeJSON(w, saved)
}

func (s *GameService) handleListStudies(w http.ResponseWriter, r *http.Request) {
	uid, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rows, err := s.db.ListStudiesForUser(uid)
	if err != nil {
		slog.Error("list studies failed", "user_id", uid, "error", err)
		http.Error(w, "failed to list studies", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.Study{}
	}
	writeJSON(w, rows)
}

// userMayReadStudy is the spectator-aware predicate for read paths:
// owner always passes; anyone (including unauthenticated callers,
// which arrive with uid=0) passes if the row's is_public flag is set.
// Mutations (PATCH, DELETE, visibility) keep using the strict owner
// check — public-flagged studies can be read by anyone but only
// modified by their owner.
func userMayReadStudy(uid int64, st *db.Study) bool {
	if st == nil {
		return false
	}
	if st.IsPublic {
		return true
	}
	return uid != 0 && st.UserID == uid
}

func (s *GameService) handleGetStudy(w http.ResponseWriter, r *http.Request) {
	// uid=0 is fine for public studies; only private studies need the
	// signed-in path. authedUserID returns (0, false) for anonymous
	// callers, which the predicate handles via the IsPublic branch.
	uid, _ := authedUserID(r)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid study id", http.StatusBadRequest)
		return
	}
	st, err := s.db.GetStudy(id)
	if err != nil || !userMayReadStudy(uid, st) {
		// Existence-leak rule: a non-owner of a private study sees the
		// same 404 as a real miss. Public studies are openly readable.
		http.Error(w, "study not found", http.StatusNotFound)
		return
	}
	writeJSON(w, st)
}

func (s *GameService) handleUpdateStudy(w http.ResponseWriter, r *http.Request) {
	uid, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid study id", http.StatusBadRequest)
		return
	}
	var req updateStudyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > studyMaxNameBytes {
		http.Error(w, "name required (1.."+strconv.Itoa(studyMaxNameBytes)+" chars)", http.StatusBadRequest)
		return
	}
	tree, err := validateStudyTree(req.Tree)
	if err != nil {
		http.Error(w, "invalid tree: "+err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := s.db.UpdateStudy(id, uid, req.Name, tree)
	if err != nil {
		slog.Error("update study failed", "id", id, "user_id", uid, "error", err)
		http.Error(w, "failed to update study", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		http.Error(w, "study not found", http.StatusNotFound)
		return
	}
	// Re-read so the response carries the bumped updated_at.
	st, _ := s.db.GetStudy(id)
	writeJSON(w, st)
}

// handleGetStudyPositions returns FENs for every node along the study
// tree's main chain (follow first child at each level). Index 0 is
// the study's start FEN; index k is the position after the k-th move
// in the main line. Lets the SPA scrub the study viewer locally
// without bundling a JS chess engine — we already have pkg/core here,
// so one server roundtrip per study viewer load is enough.
//
// Ownership is enforced the same way GetStudy does it: non-owner
// returns 404 (existence-leak rule). A malformed stored tree or an
// unparseable LAN move halts the chain early and returns what was
// successfully replayed — viewer falls back to displaying as many
// positions as it got.
func (s *GameService) handleGetStudyPositions(w http.ResponseWriter, r *http.Request) {
	// Auth-optional: public studies are scrubble by anyone with the
	// link; private studies stay owner-only.
	uid, _ := authedUserID(r)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid study id", http.StatusBadRequest)
		return
	}
	st, err := s.db.GetStudy(id)
	if err != nil || !userMayReadStudy(uid, st) {
		http.Error(w, "study not found", http.StatusNotFound)
		return
	}

	var root studyTreeNode
	if err := json.Unmarshal(st.Tree, &root); err != nil {
		slog.Error("study tree unmarshal failed", "id", id, "error", err)
		http.Error(w, "invalid stored tree", http.StatusInternalServerError)
		return
	}

	// Collect main-chain LAN moves (first child at each level).
	mainChain := []string{}
	cur := root
	for len(cur.Children) > 0 {
		next := cur.Children[0]
		mainChain = append(mainChain, next.Move)
		cur = next
	}

	board, err := core.ParseFEN(st.StartFEN)
	if err != nil {
		slog.Error("study start FEN parse failed", "id", id, "fen", st.StartFEN, "error", err)
		http.Error(w, "invalid stored FEN", http.StatusInternalServerError)
		return
	}
	fens := []string{board.FEN()}
	for _, lan := range mainChain {
		mv, perr := board.ParseUCIMove(lan)
		if perr != nil {
			// A corrupt move in the stored tree — return what we have
			// and let the viewer render up to that point.
			slog.Warn("study chain replay halted", "id", id, "ply", len(fens), "move", lan, "error", perr)
			break
		}
		board.MakeMove(mv)
		fens = append(fens, board.FEN())
	}
	writeJSON(w, fens)
}

// handleSetStudyVisibility flips the is_public flag (owner only).
// Body: {"is_public": true|false}. Returns the updated row so the SPA
// can reflect the new visibility (and surface the share link) without
// a second GET round-trip.
func (s *GameService) handleSetStudyVisibility(w http.ResponseWriter, r *http.Request) {
	uid, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid study id", http.StatusBadRequest)
		return
	}
	var req struct {
		IsPublic bool `json:"is_public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := s.db.SetStudyVisibility(id, uid, req.IsPublic)
	if err != nil {
		slog.Error("set study visibility failed", "id", id, "user_id", uid, "error", err)
		http.Error(w, "failed to update study", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		// 0 rows = not yours or not found; identical 404 either way.
		http.Error(w, "study not found", http.StatusNotFound)
		return
	}
	slog.Info("study visibility updated", "id", id, "user_id", uid, "is_public", req.IsPublic)
	st, _ := s.db.GetStudy(id)
	writeJSON(w, st)
}

func (s *GameService) handleDeleteStudy(w http.ResponseWriter, r *http.Request) {
	uid, ok := authedUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid study id", http.StatusBadRequest)
		return
	}
	rows, err := s.db.DeleteStudy(id, uid)
	if err != nil {
		slog.Error("delete study failed", "id", id, "user_id", uid, "error", err)
		http.Error(w, "failed to delete study", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		// Same 404 for "not yours" as for "not found".
		http.Error(w, "study not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
