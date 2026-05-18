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

func (s *GameService) handleGetStudy(w http.ResponseWriter, r *http.Request) {
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
	st, err := s.db.GetStudy(id)
	if err != nil || st == nil || st.UserID != uid {
		// Existence-leak rule: a non-owner sees the same 404 as a real
		// miss. Internal telemetry can distinguish via logs.
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
