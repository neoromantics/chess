package main

// Studies surface — round-trip + ownership tests against an in-memory
// store stub. The handlers themselves stay simple (no engine logic, no
// per-game locks), so the worthwhile coverage is the auth gate, the
// tree-validation rejections, and the non-owner-as-404 behaviour.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/neoromantics/chess/pkg/db"
)

// studyStore composes panicStore with an in-memory map of studies.
// Mirrors the gameStore pattern: deep-copy via JSON round-trip on read
// so handler-side mutations don't leak back into the store.
type studyStore struct {
	panicStore
	rows map[uuid.UUID]*db.Study
}

func newStudyStore() *studyStore { return &studyStore{rows: make(map[uuid.UUID]*db.Study)} }

func (s *studyStore) CreateStudy(in *db.Study) (*db.Study, error) {
	out := *in
	if out.ID == uuid.Nil {
		out.ID = uuid.New()
	}
	if out.CreatedAt.IsZero() {
		out.CreatedAt = time.Now()
	}
	out.UpdatedAt = out.CreatedAt
	if len(out.Tree) == 0 {
		out.Tree = json.RawMessage(`{"children":[]}`)
	}
	stored := out
	s.rows[stored.ID] = &stored
	cpy := stored
	return &cpy, nil
}

func (s *studyStore) GetStudy(id uuid.UUID) (*db.Study, error) {
	row, ok := s.rows[id]
	if !ok {
		return nil, errNotFoundStub
	}
	cpy := *row
	return &cpy, nil
}

func (s *studyStore) ListStudiesForUser(uid int64) ([]db.Study, error) {
	var out []db.Study
	for _, r := range s.rows {
		if r.UserID == uid {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (s *studyStore) UpdateStudy(id uuid.UUID, uid int64, name string, tree json.RawMessage, positionLabel string) (int64, error) {
	row, ok := s.rows[id]
	if !ok || row.UserID != uid {
		return 0, nil
	}
	row.Name = name
	row.Tree = tree
	row.PositionLabel = positionLabel
	row.UpdatedAt = time.Now()
	return 1, nil
}

func (s *studyStore) SetStudyVisibility(id uuid.UUID, uid int64, isPublic bool) (int64, error) {
	row, ok := s.rows[id]
	if !ok || row.UserID != uid {
		return 0, nil
	}
	row.IsPublic = isPublic
	row.UpdatedAt = time.Now()
	return 1, nil
}

func (s *studyStore) DeleteStudy(id uuid.UUID, uid int64) (int64, error) {
	row, ok := s.rows[id]
	if !ok || row.UserID != uid {
		return 0, nil
	}
	delete(s.rows, id)
	return 1, nil
}

// addUserHeader is the X-User-ID injection the gateway does for every
// proxied call. authedUserID reads it; without the header, handlers
// 401 — which is the unauthorized branch the tests cover separately.
func addUserHeader(r *http.Request, uid int64) {
	r.Header.Set("X-User-ID", strconv.FormatInt(uid, 10))
}

func TestStudyRoundTrip(t *testing.T) {
	store := newStudyStore()
	svc, _ := newTestService(t, store)

	// 1) CREATE: linear-line tree (one parent → one child), captures
	// the "save my game's move history as a session" flow.
	body := []byte(`{
		"name": "King's Pawn line",
		"start_fen": "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
		"tree": {"children": [{"move":"e2e4","san":"e4","children":[
			{"move":"e7e5","san":"e5","children":[]}
		]}]}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/studies", bytes.NewReader(body))
	addUserHeader(req, 42)
	w := httptest.NewRecorder()
	svc.handleCreateStudy(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create: status=%d body=%s", w.Code, w.Body.String())
	}
	var created db.Study
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}
	if created.UserID != 42 || created.Name != "King's Pawn line" {
		t.Fatalf("create body mismatch: %+v", created)
	}

	// 2) GET as owner: returns the same row.
	req = httptest.NewRequest(http.MethodGet, "/api/studies/"+created.ID.String(), nil)
	req.SetPathValue("id", created.ID.String())
	addUserHeader(req, 42)
	w = httptest.NewRecorder()
	svc.handleGetStudy(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get-own: status=%d body=%s", w.Code, w.Body.String())
	}

	// 3) GET as non-owner: 404 (existence-leak rule, NOT 403).
	req = httptest.NewRequest(http.MethodGet, "/api/studies/"+created.ID.String(), nil)
	req.SetPathValue("id", created.ID.String())
	addUserHeader(req, 99)
	w = httptest.NewRecorder()
	svc.handleGetStudy(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get-non-owner: want 404, got %d", w.Code)
	}

	// 4) PATCH: rename + replace tree.
	patch := []byte(`{"name":"Renamed","tree":{"children":[]}}`)
	req = httptest.NewRequest(http.MethodPatch, "/api/studies/"+created.ID.String(), bytes.NewReader(patch))
	req.SetPathValue("id", created.ID.String())
	addUserHeader(req, 42)
	w = httptest.NewRecorder()
	svc.handleUpdateStudy(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update: status=%d body=%s", w.Code, w.Body.String())
	}
	var updated db.Study
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("unmarshal updated: %v", err)
	}
	if updated.Name != "Renamed" {
		t.Fatalf("update name mismatch: %q", updated.Name)
	}

	// 5) PATCH as non-owner: 404. UpdateStudy returns 0 affected rows
	// because the user_id scope rejects the write.
	req = httptest.NewRequest(http.MethodPatch, "/api/studies/"+created.ID.String(), bytes.NewReader(patch))
	req.SetPathValue("id", created.ID.String())
	addUserHeader(req, 99)
	w = httptest.NewRecorder()
	svc.handleUpdateStudy(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("update-non-owner: want 404, got %d", w.Code)
	}

	// 6) DELETE as owner.
	req = httptest.NewRequest(http.MethodDelete, "/api/studies/"+created.ID.String(), nil)
	req.SetPathValue("id", created.ID.String())
	addUserHeader(req, 42)
	w = httptest.NewRecorder()
	svc.handleDeleteStudy(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestStudyTreeValidation(t *testing.T) {
	store := newStudyStore()
	svc, _ := newTestService(t, store)
	cases := []struct {
		name     string
		body     string
		wantCode int
	}{
		{
			name:     "missing name",
			body:     `{"name":"","start_fen":"x","tree":{"children":[]}}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "missing fen",
			body:     `{"name":"x","start_fen":"","tree":{"children":[]}}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "tree carries a move at root",
			body:     `{"name":"x","start_fen":"y","tree":{"move":"e2e4","children":[]}}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "tree has unknown field",
			body:     `{"name":"x","start_fen":"y","tree":{"children":[],"unknown":1}}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "no tree → defaults to empty children OK",
			body:     `{"name":"x","start_fen":"y"}`,
			wantCode: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/studies", bytes.NewReader([]byte(tc.body)))
			addUserHeader(req, 42)
			w := httptest.NewRecorder()
			svc.handleCreateStudy(w, req)
			if w.Code != tc.wantCode {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestStudyUnauthorized(t *testing.T) {
	store := newStudyStore()
	svc, _ := newTestService(t, store)
	// No X-User-ID → 401 on the mutation + list endpoints. The per-id
	// reads (handleGetStudy / handleGetStudyPositions) are excluded
	// here — they're auth-optional now to support public-link reads;
	// their behaviour on private studies (404) is covered by the
	// round-trip test's "get-non-owner: 404" assertion.
	for _, fn := range []func(http.ResponseWriter, *http.Request){
		svc.handleCreateStudy,
		svc.handleListStudies,
		svc.handleUpdateStudy,
		svc.handleSetStudyVisibility,
		svc.handleDeleteStudy,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/studies", nil)
		req.SetPathValue("id", uuid.New().String())
		w := httptest.NewRecorder()
		fn(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", w.Code)
		}
	}
}

func TestStudyPublicRead(t *testing.T) {
	store := newStudyStore()
	svc, _ := newTestService(t, store)
	// Seed a study owned by user 42.
	body := []byte(`{"name":"Sicilian main line","start_fen":"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1","tree":{"children":[]}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/studies", bytes.NewReader(body))
	addUserHeader(req, 42)
	w := httptest.NewRecorder()
	svc.handleCreateStudy(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("seed: %d", w.Code)
	}
	var created db.Study
	_ = json.Unmarshal(w.Body.Bytes(), &created)

	// Anonymous GET on a still-private study → 404 (existence leak rule).
	req = httptest.NewRequest(http.MethodGet, "/api/studies/"+created.ID.String(), nil)
	req.SetPathValue("id", created.ID.String())
	w = httptest.NewRecorder()
	svc.handleGetStudy(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("anon-private: want 404, got %d", w.Code)
	}

	// Owner flips it public.
	vis := []byte(`{"is_public":true}`)
	req = httptest.NewRequest(http.MethodPost, "/api/studies/"+created.ID.String()+"/visibility", bytes.NewReader(vis))
	req.SetPathValue("id", created.ID.String())
	addUserHeader(req, 42)
	w = httptest.NewRecorder()
	svc.handleSetStudyVisibility(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("visibility: %d body=%s", w.Code, w.Body.String())
	}

	// Anonymous GET on the now-public study → 200.
	req = httptest.NewRequest(http.MethodGet, "/api/studies/"+created.ID.String(), nil)
	req.SetPathValue("id", created.ID.String())
	w = httptest.NewRecorder()
	svc.handleGetStudy(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("anon-public: want 200, got %d body=%s", w.Code, w.Body.String())
	}

	// Non-owner can't flip it back (404 not 403).
	flipBack := []byte(`{"is_public":false}`)
	req = httptest.NewRequest(http.MethodPost, "/api/studies/"+created.ID.String()+"/visibility", bytes.NewReader(flipBack))
	req.SetPathValue("id", created.ID.String())
	addUserHeader(req, 99)
	w = httptest.NewRecorder()
	svc.handleSetStudyVisibility(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("non-owner visibility: want 404, got %d", w.Code)
	}
}
