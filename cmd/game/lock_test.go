package main

// Per-game lock smoke tests. The lock is the only thing standing
// between two replicas writing to the same game row concurrently —
// every regression here is catastrophic, but the primitive itself is
// 80 lines and easy to test against an in-process Redis.

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestAcquireGameLock_BasicAcquire(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()

	lock, err := acquireGameLock(ctx, rdb, "game-a", 5*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lock == nil {
		t.Fatal("acquire returned nil lock on uncontested key")
	}
	if err := lock.release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestAcquireGameLock_BusyKeyReturnsNil(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()

	first, err := acquireGameLock(ctx, rdb, "game-a", 5*time.Second)
	if err != nil || first == nil {
		t.Fatalf("first acquire: lock=%v err=%v", first, err)
	}

	second, err := acquireGameLock(ctx, rdb, "game-a", 5*time.Second)
	if err != nil {
		t.Fatalf("second acquire returned error: %v", err)
	}
	if second != nil {
		t.Fatalf("second acquire admitted while first still held — contention isn't serialized")
	}
}

func TestRelease_OnlyDeletesOwnToken(t *testing.T) {
	// Two callers, two tokens. Caller A's release must not blow away
	// caller B's lock — the compare-and-delete Lua script is what
	// stops a slow holder whose TTL expired from killing the
	// successor's lock when it finally tries to release.
	rdb := newTestRedis(t)
	ctx := context.Background()

	a, err := acquireGameLock(ctx, rdb, "game-a", 5*time.Second)
	if err != nil || a == nil {
		t.Fatalf("a acquire: %v", err)
	}

	// Forge a "stale holder" by reaching into the lock value: set the
	// key to a fake token so caller A's release becomes a no-op (it
	// no longer owns the key).
	if err := rdb.Set(ctx, a.key, "different-token", 5*time.Second).Err(); err != nil {
		t.Fatalf("forge: %v", err)
	}

	if err := a.release(ctx); err != nil {
		t.Fatalf("a.release: %v", err)
	}
	val, err := rdb.Get(ctx, a.key).Result()
	if err != nil {
		t.Fatalf("get after stale release: %v", err)
	}
	if val != "different-token" {
		t.Errorf("stale release deleted the successor's token; want 'different-token', got %q", val)
	}
}
