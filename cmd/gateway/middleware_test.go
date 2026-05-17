package main

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestClientIP(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		remote  string
		want    string
	}{
		{
			name:    "X-Real-IP wins over X-Forwarded-For and RemoteAddr",
			headers: map[string]string{"X-Real-IP": "203.0.113.5", "X-Forwarded-For": "10.0.0.1"},
			remote:  "192.168.1.1:54321",
			want:    "203.0.113.5",
		},
		{
			name:    "X-Forwarded-For leftmost when X-Real-IP missing",
			headers: map[string]string{"X-Forwarded-For": "203.0.113.5, 10.0.0.1, 172.16.0.1"},
			remote:  "192.168.1.1:54321",
			want:    "203.0.113.5",
		},
		{
			name:    "X-Forwarded-For single entry",
			headers: map[string]string{"X-Forwarded-For": "203.0.113.5"},
			remote:  "192.168.1.1:54321",
			want:    "203.0.113.5",
		},
		{
			name:    "RemoteAddr fallback strips port",
			headers: nil,
			remote:  "192.168.1.1:54321",
			want:    "192.168.1.1",
		},
		{
			name:    "RemoteAddr without port returned verbatim",
			headers: nil,
			remote:  "192.168.1.1",
			want:    "192.168.1.1",
		},
		{
			name:    "Empty X-Real-IP falls through to X-Forwarded-For",
			headers: map[string]string{"X-Real-IP": "  ", "X-Forwarded-For": "203.0.113.5"},
			remote:  "192.168.1.1:54321",
			want:    "203.0.113.5",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{
				Header:     http.Header{},
				RemoteAddr: tc.remote,
			}
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := clientIP(r); got != tc.want {
				t.Errorf("clientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLimiterAllowBurst(t *testing.T) {
	// Burst of 3, refill rate is irrelevant for this test — we just
	// want to confirm the per-key bucket admits exactly `burst`
	// requests in tight succession and then denies.
	l := NewLimiter(0.001, 3)

	for i := 0; i < 3; i++ {
		if !l.Allow("203.0.113.5") {
			t.Errorf("Allow() denied within burst at i=%d", i)
		}
	}
	if l.Allow("203.0.113.5") {
		t.Errorf("Allow() admitted past burst")
	}
}

func TestLimiterIsolatesKeys(t *testing.T) {
	l := NewLimiter(0.001, 1)
	if !l.Allow("a") {
		t.Fatal("Allow(a) denied initial token")
	}
	if l.Allow("a") {
		t.Fatal("Allow(a) admitted past burst")
	}
	// Different key gets its own bucket.
	if !l.Allow("b") {
		t.Fatal("Allow(b) denied initial token — key isolation broken")
	}
}

func TestLimiterConcurrentMint(t *testing.T) {
	// Hammer the same key from many goroutines simultaneously. The
	// double-checked-lock around bucket creation must not crash or
	// hand out duplicate buckets. We verify by confirming we never get
	// more admits than the burst.
	l := NewLimiter(0.001, 5)
	var wg sync.WaitGroup
	var admitted int64
	var mu sync.Mutex
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow("hotkey") {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted > 5 {
		t.Errorf("concurrent Allow admitted %d, want <= 5 (burst)", admitted)
	}
}

func TestLimiterJanitorEvicts(t *testing.T) {
	l := NewLimiter(0.001, 1)
	_ = l.Allow("ephemeral")

	l.mu.RLock()
	beforeSize := len(l.buckets)
	l.mu.RUnlock()
	if beforeSize != 1 {
		t.Fatalf("expected 1 bucket before eviction, got %d", beforeSize)
	}

	stop := make(chan struct{})
	defer close(stop)
	// idle=1ns means everything is stale; period=1ms means the first
	// tick fires fast. Wait two ticks to be safe.
	go l.RunJanitor(stop, time.Nanosecond, time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	l.mu.RLock()
	afterSize := len(l.buckets)
	l.mu.RUnlock()
	if afterSize != 0 {
		t.Errorf("janitor left %d buckets, want 0", afterSize)
	}
}
