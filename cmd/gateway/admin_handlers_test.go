package main

// Tests for handleAdminDeleteUser.
//
// Why these specifically: it's the only destructive admin endpoint we
// have, runs against rows that cascade-NULL into games / cancel-CASCADE
// into invites, and the only authz gate is is_admin=TRUE in the DB +
// the typed-confirm body. A silent regression on any of the guards
// (self-delete, bot-delete, confirm-mismatch, audit-before-cascade) is
// an actual production hazard, and the handler had zero coverage.
//
// Style: stdlib testing, no third-party mocking. The full Store
// interface is satisfied by panicStore (every method panics) so any
// unintended DB call inside the handler fails the test loudly instead
// of being silently swallowed. fakeStore embeds panicStore and
// overrides only the three methods this handler exercises.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/db"
)

// capturedAction records what InsertAdminAction was called with, so a
// test can assert the audit row was written with the expected actor/
// target/detail before the cascade ran.
type capturedAction struct {
	ActorID        *int64
	ActorUsername  string
	Action         string
	TargetID       *int64
	TargetUsername string
	Detail         string
}

// fakeStore embeds panicStore so the 30+ unused Store methods crash
// loudly on accidental access. The fields below capture the calls
// handleAdminDeleteUser actually makes; per-test setup wires them.
type fakeStore struct {
	panicStore

	users map[int64]*db.User

	auditWriteFails bool
	deleteUserFails bool

	actions     []capturedAction
	deletedIDs  []int64
	deletedRows int64 // value DeleteUser returns; default 1 unless overridden
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:       make(map[int64]*db.User),
		deletedRows: 1,
	}
}

func (f *fakeStore) addUser(u *db.User) { f.users[u.ID] = u }
func (f *fakeStore) GetUserByID(id int64) (*db.User, error) {
	u, ok := f.users[id]
	if !ok {
		// Mirrors PostgresStore.GetUserByID's "not found" return —
		// the handler does `target == nil || err != nil` → 404.
		return nil, nil
	}
	return u, nil
}

func (f *fakeStore) InsertAdminAction(actorID *int64, actorUsername, action string, targetID *int64, targetUsername, detail string) error {
	if f.auditWriteFails {
		return errors.New("audit write injected failure")
	}
	f.actions = append(f.actions, capturedAction{
		ActorID: actorID, ActorUsername: actorUsername,
		Action:   action,
		TargetID: targetID, TargetUsername: targetUsername,
		Detail: detail,
	})
	return nil
}

func (f *fakeStore) DeleteUser(id int64) (int64, error) {
	if f.deleteUserFails {
		return 0, errors.New("delete injected failure")
	}
	f.deletedIDs = append(f.deletedIDs, id)
	return f.deletedRows, nil
}

// adminUser returns a db.User wired as an admin with the given id /
// username. Same shape every test uses.
func adminUser(id int64, username string) *db.User {
	return &db.User{ID: id, Username: username, IsAdmin: true, Rating: 1500}
}

func regularUser(id int64, username string) *db.User {
	return &db.User{ID: id, Username: username, IsAdmin: false, Rating: 1500}
}

// withAuthedUser returns a request with auth.Claims injected into
// context, matching what auth.Middleware does at runtime.
func withAuthedUser(req *http.Request, c *auth.Claims) *http.Request {
	if c == nil {
		return req
	}
	ctx := context.WithValue(req.Context(), auth.UserContextKey, c)
	return req.WithContext(ctx)
}

// newDeleteRequest builds DELETE /api/admin/users/{id} with a JSON
// body. PathValue("id") is set so the handler's r.PathValue works
// without us mounting a mux just to populate it.
func newDeleteRequest(t *testing.T, targetID int64, confirm string) *http.Request {
	t.Helper()
	body := strings.NewReader(`{"confirm_username":"` + confirm + `"}`)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/users/x", body)
	req.SetPathValue("id", itoa(targetID))
	return req
}

func itoa(n int64) string {
	// Avoid strconv import just for this; tests stay tight.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// newGateway builds a minimal Gateway. Only db is wired — every other
// field is left zero because handleAdminDeleteUser never touches them.
func newGateway(store db.Store) *Gateway {
	return &Gateway{db: store}
}

// ---- Tests ----

// Non-admin caller. adminOnly re-fetches the user row and sees
// IsAdmin=false, so we get 404 (not 403) — same existence-hiding
// convention the rest of the platform uses.
func TestAdminDeleteUser_NonAdmin404(t *testing.T) {
	store := newFakeStore()
	store.addUser(regularUser(7, "alice")) // caller
	store.addUser(regularUser(8, "bob"))   // target

	req := newDeleteRequest(t, 8, "bob")
	req = withAuthedUser(req, &auth.Claims{UserID: 7, Username: "alice"})
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("non-admin should get 404, got %d", w.Code)
	}
	if len(store.actions) != 0 || len(store.deletedIDs) != 0 {
		t.Fatalf("non-admin path should not have written audit (%d) or deleted (%d)",
			len(store.actions), len(store.deletedIDs))
	}
}

// Anonymous caller (no auth context). adminOnly's GetUser miss also
// returns 404.
func TestAdminDeleteUser_Anonymous404(t *testing.T) {
	store := newFakeStore()
	store.addUser(regularUser(8, "bob"))

	req := newDeleteRequest(t, 8, "bob")
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("anonymous should get 404, got %d", w.Code)
	}
}

// An admin can't delete themselves. The handler blocks this explicitly
// because it's irrecoverable (operator would 404 their own /admin
// route afterwards).
func TestAdminDeleteUser_SelfDelete400(t *testing.T) {
	store := newFakeStore()
	store.addUser(adminUser(7, "alice"))

	req := newDeleteRequest(t, 7, "alice")
	req = withAuthedUser(req, &auth.Claims{UserID: 7, Username: "alice"})
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("self-delete should be 400, got %d", w.Code)
	}
	if len(store.deletedIDs) != 0 {
		t.Fatalf("self-delete must not delete (got %v)", store.deletedIDs)
	}
}

// A seeded bot can't be deleted via the admin path. The matchmaker
// holds the in-memory pool and would dispatch into a missing row.
func TestAdminDeleteUser_Bot400(t *testing.T) {
	store := newFakeStore()
	store.addUser(adminUser(7, "alice"))
	store.addUser(regularUser(99, "QueenKnight42")) // a real seeded bot username

	req := newDeleteRequest(t, 99, "QueenKnight42")
	req = withAuthedUser(req, &auth.Claims{UserID: 7, Username: "alice"})
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("bot delete should be 400, got %d", w.Code)
	}
	if len(store.deletedIDs) != 0 {
		t.Fatalf("bot delete must not delete (got %v)", store.deletedIDs)
	}
}

// Missing target — admin valid, but the id points to no row.
func TestAdminDeleteUser_MissingTarget404(t *testing.T) {
	store := newFakeStore()
	store.addUser(adminUser(7, "alice"))

	req := newDeleteRequest(t, 1234, "ghost")
	req = withAuthedUser(req, &auth.Claims{UserID: 7, Username: "alice"})
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("missing target should be 404, got %d", w.Code)
	}
}

// Typed-confirm body doesn't match target username. Same guard as
// `git branch -D <name>` — typo protection on a destructive op.
func TestAdminDeleteUser_ConfirmMismatch400(t *testing.T) {
	store := newFakeStore()
	store.addUser(adminUser(7, "alice"))
	store.addUser(regularUser(8, "bob"))

	req := newDeleteRequest(t, 8, "wrong_username")
	req = withAuthedUser(req, &auth.Claims{UserID: 7, Username: "alice"})
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("confirm mismatch should be 400, got %d", w.Code)
	}
	if len(store.actions) != 0 || len(store.deletedIDs) != 0 {
		t.Fatalf("confirm mismatch must not write audit or delete")
	}
}

// Bad id in path. Belt-and-braces — handler parses with strconv and
// rejects values that can't be int64 or are <= 0.
func TestAdminDeleteUser_BadID400(t *testing.T) {
	store := newFakeStore()
	store.addUser(adminUser(7, "alice"))

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/users/x",
		strings.NewReader(`{"confirm_username":"whatever"}`))
	req.SetPathValue("id", "not-a-number")
	req = withAuthedUser(req, &auth.Claims{UserID: 7, Username: "alice"})
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad id should be 400, got %d", w.Code)
	}
}

// Happy path: admin deletes a regular user. Returns 204, writes the
// audit row, runs the cascade. Audit must land BEFORE delete so the
// trail survives even if the cascade itself crashes.
func TestAdminDeleteUser_HappyPath204(t *testing.T) {
	store := newFakeStore()
	store.addUser(adminUser(7, "alice"))
	store.addUser(&db.User{ID: 8, Username: "bob", Rating: 1432})

	req := newDeleteRequest(t, 8, "bob")
	req = withAuthedUser(req, &auth.Claims{UserID: 7, Username: "alice"})
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("happy path should be 204, got %d (body=%q)", w.Code, w.Body.String())
	}
	if len(store.actions) != 1 {
		t.Fatalf("expected exactly one audit row, got %d", len(store.actions))
	}
	a := store.actions[0]
	if a.Action != "delete_user" {
		t.Errorf("audit Action=%q, want delete_user", a.Action)
	}
	if a.ActorUsername != "alice" {
		t.Errorf("audit ActorUsername=%q, want alice", a.ActorUsername)
	}
	if a.TargetUsername != "bob" {
		t.Errorf("audit TargetUsername=%q, want bob", a.TargetUsername)
	}
	if a.ActorID == nil || *a.ActorID != 7 {
		t.Errorf("audit ActorID=%v, want *7", a.ActorID)
	}
	if a.TargetID == nil || *a.TargetID != 8 {
		t.Errorf("audit TargetID=%v, want *8", a.TargetID)
	}
	// Detail should mention the target's rating — useful breadcrumb if
	// we ever need to reconstruct what was deleted.
	if !strings.Contains(a.Detail, "1432") {
		t.Errorf("audit Detail=%q should mention target rating 1432", a.Detail)
	}
	if got := store.deletedIDs; len(got) != 1 || got[0] != 8 {
		t.Errorf("expected DeleteUser(8), got %v", got)
	}
}

// Audit-write failure short-circuits before delete. Critical: we never
// want to lose the row without leaving a trail of who tried to remove
// it.
func TestAdminDeleteUser_AuditFailureBlocksCascade(t *testing.T) {
	store := newFakeStore()
	store.addUser(adminUser(7, "alice"))
	store.addUser(regularUser(8, "bob"))
	store.auditWriteFails = true

	req := newDeleteRequest(t, 8, "bob")
	req = withAuthedUser(req, &auth.Claims{UserID: 7, Username: "alice"})
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("audit failure should be 500, got %d", w.Code)
	}
	if len(store.deletedIDs) != 0 {
		t.Fatalf("audit failure must NOT proceed to delete (got %v)", store.deletedIDs)
	}
}

// Delete failure after audit row written. Audit row stays (we can't
// roll it back, and we wouldn't want to — the attempt happened). 500
// to the caller. The audit log is consistent with "tried and failed".
func TestAdminDeleteUser_DeleteFailureLeavesAuditRow(t *testing.T) {
	store := newFakeStore()
	store.addUser(adminUser(7, "alice"))
	store.addUser(regularUser(8, "bob"))
	store.deleteUserFails = true

	req := newDeleteRequest(t, 8, "bob")
	req = withAuthedUser(req, &auth.Claims{UserID: 7, Username: "alice"})
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete failure should be 500, got %d", w.Code)
	}
	if len(store.actions) != 1 {
		t.Fatalf("audit row should still exist after delete failure, got %d", len(store.actions))
	}
}

// Malformed JSON body — 400 before the typed-confirm check.
func TestAdminDeleteUser_MalformedBody400(t *testing.T) {
	store := newFakeStore()
	store.addUser(adminUser(7, "alice"))
	store.addUser(regularUser(8, "bob"))

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/users/8",
		bytes.NewReader([]byte(`{not-json`)))
	req.SetPathValue("id", "8")
	req = withAuthedUser(req, &auth.Claims{UserID: 7, Username: "alice"})
	w := httptest.NewRecorder()

	newGateway(store).handleAdminDeleteUser(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body should be 400, got %d", w.Code)
	}
}

// ---- Store stubs ----
//
// panicStore satisfies db.Store with panics on every method. fakeStore
// embeds it and overrides only what handleAdminDeleteUser uses. A
// future handler change that calls additional Store methods inside
// this code path will panic loudly here instead of silently passing.

type panicStore struct{}

func (panicStore) Close() error { panic("unexpected Close") }
func (panicStore) Ping() error  { panic("unexpected Ping") }

func (panicStore) CreateUser(string, string) (*db.User, error) {
	panic("unexpected CreateUser")
}
func (panicStore) GetUserByUsername(string) (*db.User, error) {
	panic("unexpected GetUserByUsername")
}
func (panicStore) GetUserByID(int64) (*db.User, error) { panic("unexpected GetUserByID") }
func (panicStore) SearchUsersByPrefix(string) ([]db.UserSummary, error) {
	panic("unexpected SearchUsersByPrefix")
}
func (panicStore) UpdateUserProfile(int64, string, string, string, string) error {
	panic("unexpected UpdateUserProfile")
}
func (panicStore) UpdateLastLogin(int64) error        { panic("unexpected UpdateLastLogin") }
func (panicStore) UpdatePassword(int64, string) error { panic("unexpected UpdatePassword") }
func (panicStore) UpdateUserRating(db.RatingUpdate) error {
	panic("unexpected UpdateUserRating")
}
func (panicStore) GetUserStats(int64) (*db.UserStats, error) { panic("unexpected GetUserStats") }
func (panicStore) UpsertBot(string, string, int) (db.BotUser, error) {
	panic("unexpected UpsertBot")
}
func (panicStore) ListBots() ([]db.BotUser, error)           { panic("unexpected ListBots") }
func (panicStore) CountUsers() (int64, int64, error)         { panic("unexpected CountUsers") }
func (panicStore) CountRecentSignups() (int64, int64, error) { panic("unexpected CountRecentSignups") }
func (panicStore) ListBotStats() ([]db.BotStat, error)       { panic("unexpected ListBotStats") }
func (panicStore) ListRecentSignups() ([]db.AdminSignup, error) {
	panic("unexpected ListRecentSignups")
}
func (panicStore) CountActiveGames() (int64, error) { panic("unexpected CountActiveGames") }
func (panicStore) DeleteUser(int64) (int64, error)  { panic("unexpected DeleteUser") }
func (panicStore) InsertAdminAction(*int64, string, string, *int64, string, string) error {
	panic("unexpected InsertAdminAction")
}
func (panicStore) ListAdminActions() ([]db.AdminAction, error) {
	panic("unexpected ListAdminActions")
}
func (panicStore) ListAdminActionsBefore(time.Time) ([]db.AdminAction, error) {
	panic("unexpected ListAdminActionsBefore")
}
func (panicStore) ListActiveGamesAdmin() ([]db.AdminLiveGame, error) {
	panic("unexpected ListActiveGamesAdmin")
}
func (panicStore) SaveGame(*db.GameRecord) error { panic("unexpected SaveGame") }
func (panicStore) ListGames(int64, time.Time, int) ([]db.GameRecord, error) {
	panic("unexpected ListGames")
}
func (panicStore) GetGame(string) (*db.GameRecord, error) { panic("unexpected GetGame") }
func (panicStore) DeleteGame(string) (int64, error)       { panic("unexpected DeleteGame") }
func (panicStore) SetGameVisibility(string, bool) (int64, error) {
	panic("unexpected SetGameVisibility")
}
func (panicStore) CreateInvite(uuid.UUID, int64, int64, string, bool, time.Time) (*db.Invite, error) {
	panic("unexpected CreateInvite")
}
func (panicStore) GetInvite(uuid.UUID) (*db.Invite, error) { panic("unexpected GetInvite") }
func (panicStore) ListPendingInvitesForUser(int64) ([]db.Invite, error) {
	panic("unexpected ListPendingInvitesForUser")
}
func (panicStore) ListPendingInvitesFromUser(int64) ([]db.Invite, error) {
	panic("unexpected ListPendingInvitesFromUser")
}
func (panicStore) AcceptInviteWithGame(uuid.UUID, int64, *db.GameRecord) (int64, error) {
	panic("unexpected AcceptInviteWithGame")
}
func (panicStore) DeclineInvite(uuid.UUID, int64) (int64, error) {
	panic("unexpected DeclineInvite")
}
func (panicStore) CancelInvite(uuid.UUID, int64) (int64, error) {
	panic("unexpected CancelInvite")
}
func (panicStore) ExpireStaleInvites() ([]db.Invite, error) {
	panic("unexpected ExpireStaleInvites")
}
func (panicStore) CreateStudy(*db.Study) (*db.Study, error) { panic("unexpected CreateStudy") }
func (panicStore) GetStudy(uuid.UUID) (*db.Study, error)    { panic("unexpected GetStudy") }
func (panicStore) ListStudiesForUser(int64) ([]db.Study, error) {
	panic("unexpected ListStudiesForUser")
}
func (panicStore) UpdateStudy(uuid.UUID, int64, string, json.RawMessage) (int64, error) {
	panic("unexpected UpdateStudy")
}
func (panicStore) DeleteStudy(uuid.UUID, int64) (int64, error) {
	panic("unexpected DeleteStudy")
}

// Ensure panicStore satisfies db.Store at compile time.
var _ db.Store = panicStore{}

// jsonBody helper for tests that want to round-trip a typed value.
// Kept here for future test additions; unused right now is fine.
//
//nolint:unused
func jsonBody(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(b)
}
