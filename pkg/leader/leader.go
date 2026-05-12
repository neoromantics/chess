// Package leader provides Redis-backed singleton leader election so that
// "exactly one replica must run this loop" jobs (matchmaker pairing sweep,
// invite expiry sweeper, Glicko-2 rating updater) can live as goroutines
// inside the api pods without spinning up a separate microservice.
//
// The election uses SET NX EX with an opaque token. Renewal is best-effort
// via PEXPIRE; the Lua script in pkg/bus guards release so a slow holder
// past its TTL cannot blow away a successor's lease.
//
// Callers pass a Run(ctx) function; it is invoked only while this pod
// holds the lease. When the lease is lost (renewal fails, ctx cancels,
// release runs), the context passed to Run is cancelled so the loop can
// exit cleanly. The election then immediately attempts re-acquisition.
package leader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
else
	return 0
end
`)

// Election co-ordinates a single named leader role across N replicas.
//
//	e := leader.NewElection(rdb, "matchmaker", leader.WithLeaseTTL(15*time.Second))
//	go e.Run(ctx, func(leaderCtx context.Context) {
//	    // runs only on the elected pod; leaderCtx is cancelled on lease loss
//	    for { select { case <-leaderCtx.Done(): return; case <-tick.C: ... } }
//	})
type Election struct {
	rdb     *redis.Client
	key     string
	ttl     time.Duration
	retry   time.Duration
	renewAt time.Duration
	logger  *slog.Logger

	mu    sync.Mutex
	token string
}

// Option configures an Election.
type Option func(*Election)

// WithLeaseTTL overrides the default 15s lease. Renewal happens at 1/3 TTL
// so a momentary stall on the leader doesn't immediately lose the lease.
func WithLeaseTTL(d time.Duration) Option { return func(e *Election) { e.ttl = d; e.renewAt = d / 3 } }

// WithRetry sets how often a non-leader retries acquisition. Default 5s.
func WithRetry(d time.Duration) Option { return func(e *Election) { e.retry = d } }

// WithLogger plugs in a structured logger.
func WithLogger(l *slog.Logger) Option { return func(e *Election) { e.logger = l } }

// NewElection builds an Election. name must be unique per role (e.g.
// "matchmaker", "invite-sweeper").
func NewElection(rdb *redis.Client, name string, opts ...Option) *Election {
	e := &Election{
		rdb:     rdb,
		key:     "leader:" + name,
		ttl:     15 * time.Second,
		renewAt: 5 * time.Second,
		retry:   5 * time.Second,
		logger:  slog.Default().With("leader", name),
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Run blocks until ctx is cancelled, repeatedly attempting to win the
// lease and invoking fn while held. fn receives a context that is
// cancelled the moment the lease is lost so it can exit promptly.
func (e *Election) Run(ctx context.Context, fn func(context.Context)) {
	for ctx.Err() == nil {
		acquired, err := e.acquire(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			e.logger.Warn("acquire failed", "error", err)
		}
		if !acquired {
			select {
			case <-ctx.Done():
				return
			case <-time.After(e.retry):
			}
			continue
		}

		e.logger.Info("elected leader")
		leaderCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			fn(leaderCtx)
		}()

		e.holdLease(leaderCtx, cancel)
		<-done
		_ = e.release(context.Background())
		e.logger.Info("released leader")
	}
}

func (e *Election) acquire(ctx context.Context) (bool, error) {
	tok, err := newToken()
	if err != nil {
		return false, err
	}
	ok, err := e.rdb.SetNX(ctx, e.key, tok, e.ttl).Result()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	e.mu.Lock()
	e.token = tok
	e.mu.Unlock()
	return true, nil
}

// holdLease renews the lease until ctx cancels OR renewal fails (network
// partition, redis restart, key TTL expired and another pod grabbed it).
// Renewal uses PEXPIRE which is a no-op if the key vanished, so we read
// after renewal to verify our token still matches.
func (e *Election) holdLease(ctx context.Context, cancel context.CancelFunc) {
	t := time.NewTicker(e.renewAt)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.mu.Lock()
			tok := e.token
			e.mu.Unlock()
			val, err := e.rdb.Get(ctx, e.key).Result()
			if err != nil || val != tok {
				e.logger.Warn("lease lost", "error", err)
				cancel()
				return
			}
			if err := e.rdb.PExpire(ctx, e.key, e.ttl).Err(); err != nil {
				e.logger.Warn("lease renew failed", "error", err)
				cancel()
				return
			}
		}
	}
}

func (e *Election) release(ctx context.Context) error {
	e.mu.Lock()
	tok := e.token
	e.token = ""
	e.mu.Unlock()
	if tok == "" {
		return nil
	}
	return releaseScript.Run(ctx, e.rdb, []string{e.key}, tok).Err()
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
