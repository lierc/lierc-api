package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// clocked is New with a fake clock starting at t0; set moves it.
func clocked(limit int, window time.Duration) (*Limiter, func(time.Duration)) {
	l := New(limit, window)
	now := t0
	l.Now = func() time.Time { return now }
	return l, func(d time.Duration) { now = t0.Add(d) }
}

func TestTakeLimit(t *testing.T) {
	l, _ := clocked(3, time.Minute)
	for i := 0; i < 3; i++ {
		if ok, _ := l.Take("a"); !ok {
			t.Fatalf("take %d refused", i+1)
		}
	}
	if ok, retry := l.Take("a"); ok || retry != time.Minute {
		t.Errorf("4th take: %v %v, want refused, retry 1m", ok, retry)
	}
	if ok, _ := l.Take("b"); !ok {
		t.Error("another key refused")
	}
}

func TestSlidingWindow(t *testing.T) {
	l, at := clocked(3, time.Minute)
	for _, d := range []time.Duration{0, 20 * time.Second, 40 * time.Second} {
		at(d)
		if ok, _ := l.Take("a"); !ok {
			t.Fatalf("take at %v refused", d)
		}
	}
	at(59 * time.Second)
	if ok, retry := l.Take("a"); ok || retry != time.Second {
		t.Errorf("at 59s: %v %v, want refused, retry 1s", ok, retry)
	}
	at(60 * time.Second)
	if ok, _ := l.Take("a"); !ok {
		t.Error("at 60s refused: the first event should have left the window")
	}
	at(61 * time.Second)
	if ok, _ := l.Take("a"); ok {
		t.Error("at 61s allowed: 20s, 40s and 60s are still in the window")
	}
}

func TestRetryAfter(t *testing.T) {
	l, at := clocked(1, time.Minute)
	l.Take("a")
	at(time.Minute - time.Millisecond)
	if ok, retry := l.Take("a"); ok || retry <= 0 {
		t.Errorf("at the window's edge: %v %v, want refused with retry > 0", ok, retry)
	}
}

func TestReturn(t *testing.T) {
	l, _ := clocked(2, time.Minute)
	l.Take("a")
	l.Take("a")
	l.Return("a")
	if ok, _ := l.Take("a"); !ok {
		t.Error("take after a return refused")
	}
	if ok, _ := l.Take("a"); ok {
		t.Error("third take allowed")
	}
	l.Return("unknown")
}

func TestIdleKeysSwept(t *testing.T) {
	l, at := clocked(1, time.Minute)
	l.MaxKeys = 10
	for _, k := range []string{"k0", "k1", "k2", "k3", "k4"} {
		l.Take(k)
	}
	at(2 * time.Minute)
	l.Take("x")
	if n := l.len(); n != 1 {
		t.Errorf("%d keys, want 1", n)
	}
}

func TestCap(t *testing.T) {
	l, at := clocked(1, time.Hour)
	l.MaxKeys = 3
	for i, k := range []string{"a", "b", "c", "a"} {
		at(time.Duration(i) * time.Second)
		l.Take(k)
	}
	at(4 * time.Second)
	if ok, _ := l.Take("d"); !ok {
		t.Fatal("new key at the cap refused")
	}
	if n := l.len(); n != 3 {
		t.Errorf("%d keys, want 3", n)
	}
	if ok, _ := l.Take("b"); !ok {
		t.Error("b refused: it was used longest ago and should have been dropped")
	}
	if ok, _ := l.Take("a"); ok {
		t.Error("a allowed: it was used recently and should have been kept")
	}
}

func TestConcurrent(t *testing.T) {
	l := New(10, time.Minute)
	var wg sync.WaitGroup
	var n atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Take("k"); ok {
				n.Add(1)
			}
		}()
	}
	wg.Wait()
	if n.Load() != 10 {
		t.Errorf("%d takes allowed, want 10", n.Load())
	}
}
