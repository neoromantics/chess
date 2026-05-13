package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// gameLock is a Redis-backed per-game serialization primitive. The
// 6-pod backend runs N replicas of game-service that all consume the
// same command stream AND now serve sync HTTP mutations — without this
// lock, two concurrent writes to the same game_id on two pods race
// each other through GetGame → mutate → SaveGame, and last-writer-wins
// silently drops a ply.
//
// Token + Lua compare-and-delete release: a slow holder whose TTL
// expired can't blow away the successor's lock. The same correct
// Redlock-style primitive the prior monolith used.
//
// Scope of use: every code path that reads, mutates, and writes a
// single game's row must hold this lock for the duration. Read-only
// fetches (e.g. /api/state, /api/replay) don't need it.
type gameLock struct {
	rdb   *redis.Client
	key   string
	token string
}

var releaseLockScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
else
	return 0
end
`)

// acquireGameLock returns a held lock or (nil, nil) if another caller
// already holds it. ttl bounds how long we'll hold it without renewal;
// pick a value comfortably larger than the slowest expected mutation
// (engine search + DB write).
func acquireGameLock(ctx context.Context, rdb *redis.Client, gameID string, ttl time.Duration) (*gameLock, error) {
	token, err := newLockToken()
	if err != nil {
		return nil, err
	}
	key := "game:lock:" + gameID
	ok, err := rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &gameLock{rdb: rdb, key: key, token: token}, nil
}

// release no-ops if we no longer own the lock. Safe to call from defer.
func (l *gameLock) release(ctx context.Context) error {
	if l == nil {
		return nil
	}
	return releaseLockScript.Run(ctx, l.rdb, []string{l.key}, l.token).Err()
}

func newLockToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// errLocked is returned by withGameLock when another caller holds the
// lock. Callers translate this to HTTP 409 Conflict so the SPA can
// retry rather than blast the same write again.
type lockBusyError struct{ gameID string }

func (e *lockBusyError) Error() string {
	return fmt.Sprintf("game %s is locked by another writer", e.gameID)
}
