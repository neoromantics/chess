package leader

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestSingleLeaderAcrossPeers asserts that when N goroutines race to win
// the same election name, exactly one becomes leader at any moment.
//
// This is the contract that lets the matchmaker pairing sweep and the
// invite expiry sweeper live as goroutines in api pods without doing
// their work N times over.
func TestSingleLeaderAcrossPeers(t *testing.T) {
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { rdb.Close() })

	const peers = 5
	var active atomic.Int32
	var maxActive atomic.Int32

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < peers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := NewElection(rdb, "test-role",
				WithLeaseTTL(200*time.Millisecond),
				WithRetry(50*time.Millisecond))
			e.Run(ctx, func(leaderCtx context.Context) {
				n := active.Add(1)
				if n > maxActive.Load() {
					maxActive.Store(n)
				}
				<-leaderCtx.Done()
				active.Add(-1)
			})
		}()
	}
	wg.Wait()

	if got := maxActive.Load(); got != 1 {
		t.Fatalf("expected at most 1 concurrent leader, observed %d", got)
	}
}

// TestLeaseReleasedOnContextCancel asserts that when the Run-context of
// an elected pod cancels (pod death, graceful shutdown), the lease is
// released in Redis so another pod can take over. Without this, the
// role wedges for a full TTL — surprising the operator if the queue
// suddenly stops draining.
func TestLeaseReleasedOnContextCancel(t *testing.T) {
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { rdb.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	elected := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		e := NewElection(rdb, "shutdown-test",
			WithLeaseTTL(1*time.Second),
			WithRetry(50*time.Millisecond))
		e.Run(ctx, func(leaderCtx context.Context) {
			close(elected)
			<-leaderCtx.Done()
		})
	}()

	select {
	case <-elected:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("election never happened")
	}

	// Key should exist while leader is running.
	if exists, _ := rdb.Exists(context.Background(), "leader:shutdown-test").Result(); exists != 1 {
		t.Fatal("expected leader key to exist while leader is active")
	}

	cancel()
	<-done

	// After Run exits, the key must be gone — we released cleanly.
	if exists, _ := rdb.Exists(context.Background(), "leader:shutdown-test").Result(); exists != 0 {
		t.Fatal("expected leader key to be released after Run exits")
	}
}
