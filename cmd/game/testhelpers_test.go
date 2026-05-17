package main

// Shared scaffolding for game-service tests.
//
// Until this commit the package had no tests at all — every mutation
// path was validated by "I poked it in a browser." The two pieces of
// shared infrastructure here are:
//
//   - panicStore: a db.Store whose 35 methods all panic, so tests can
//                 embed it and override only the methods their handler
//                 under test actually exercises. Mirrors the pattern
//                 already used in cmd/gateway/admin_handlers_test.go.
//   - newTestService: spins up an in-process Redis (miniredis), an
//                     eventbus.Client wired to it, and a GameService
//                     bound to the caller-supplied stub store. The
//                     miniredis instance auto-cleans on t.Cleanup so
//                     tests don't leak ports or goroutines.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
)

// errNotFoundStub mirrors the production "no rows" signal — handlers
// don't compare against a specific sentinel, they just check err != nil.
var errNotFoundStub = errors.New("game not found")

// panicStore satisfies db.Store by panicking on every method. Embed and
// override only the methods that should be reachable; panics from
// unexpected calls catch over-broad fakes that quietly admit too much.
type panicStore struct{}

func (panicStore) Close() error { panic("unexpected Close") }
func (panicStore) Ping() error  { panic("unexpected Ping") }

func (panicStore) CreateUser(string, string) (*db.User, error) {
	panic("unexpected CreateUser")
}
func (panicStore) GetUserByUsername(string) (*db.User, error) { panic("unexpected GetUserByUsername") }
func (panicStore) GetUserByID(int64) (*db.User, error)        { panic("unexpected GetUserByID") }
func (panicStore) SearchUsersByPrefix(string) ([]db.UserSummary, error) {
	panic("unexpected SearchUsersByPrefix")
}
func (panicStore) UpdateUserProfile(int64, string, string, string, string) error {
	panic("unexpected UpdateUserProfile")
}
func (panicStore) UpdateLastLogin(int64) error            { panic("unexpected UpdateLastLogin") }
func (panicStore) UpdatePassword(int64, string) error     { panic("unexpected UpdatePassword") }
func (panicStore) UpdateUserRating(db.RatingUpdate) error { panic("unexpected UpdateUserRating") }
func (panicStore) GetUserStats(int64) (*db.UserStats, error) {
	panic("unexpected GetUserStats")
}
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

// gameStore is the most common stub: a single in-memory map of game
// records, keyed by ID. GetGame returns a deep copy so handler-side
// mutations can't accidentally tamper with the stored row. SaveGame
// records the latest write per ID. Tests that need richer behavior
// (forced errors, call counting) should compose their own fake.
type gameStore struct {
	panicStore
	games map[string]*db.GameRecord
	saves []db.GameRecord
}

func newGameStore() *gameStore {
	return &gameStore{games: make(map[string]*db.GameRecord)}
}

func (g *gameStore) GetGame(id string) (*db.GameRecord, error) {
	rec, ok := g.games[id]
	if !ok {
		return nil, errNotFoundStub
	}
	// Deep copy via JSON round-trip so handler mutations don't leak
	// into the store's view.
	data, _ := json.Marshal(rec)
	var out db.GameRecord
	_ = json.Unmarshal(data, &out)
	return &out, nil
}

func (g *gameStore) SaveGame(rec *db.GameRecord) error {
	data, _ := json.Marshal(rec)
	var stored db.GameRecord
	_ = json.Unmarshal(data, &stored)
	g.games[rec.ID] = &stored
	g.saves = append(g.saves, stored)
	return nil
}

// newTestService spins up a miniredis-backed GameService bound to the
// caller-supplied stub store. The miniredis instance auto-cleans on
// t.Cleanup so tests can be parallelized without port collisions.
func newTestService(t *testing.T, store db.Store) (*GameService, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	bus := eventbus.NewClient(mr.Addr())
	t.Cleanup(func() { _ = bus.Close() })
	return &GameService{db: store, bus: bus}, mr
}

// seedGame inserts a record into the store AND warms the Redis cache.
// Returns a context appropriate for use with the service.
func seedGame(t *testing.T, s *GameService, gs *gameStore, rec *db.GameRecord) context.Context {
	t.Helper()
	if rec.ID == "" {
		t.Fatal("seedGame: rec.ID must be set")
	}
	data, _ := json.Marshal(rec)
	var stored db.GameRecord
	_ = json.Unmarshal(data, &stored)
	gs.games[rec.ID] = &stored
	if err := s.writeCache(context.Background(), rec); err != nil {
		t.Fatalf("seedGame: warm cache: %v", err)
	}
	return context.Background()
}
