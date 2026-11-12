// Package ratelimit counts events per key in a sliding window, in memory.
// It's for per-account limits on login and password reset: counts are lost
// on a restart, and are per process.
package ratelimit

import (
	"container/list"
	"sync"
	"time"
)

// Limiter allows at most limit events per key in any window. Safe for
// concurrent use.
type Limiter struct {
	// MaxKeys caps the keys held; at the cap, the key used longest ago is
	// dropped to make room for a new one.
	MaxKeys int
	// Now is the clock, for tests.
	Now func() time.Time

	limit  int
	window time.Duration

	mu    sync.Mutex
	keys  map[string]*list.Element
	order *list.List // of *entry, the key used longest ago at the front
}

type entry struct {
	key    string
	events []time.Time // oldest first
}

// New is a limiter of limit events per key per window, holding at most
// 100,000 keys.
func New(limit int, window time.Duration) *Limiter {
	return &Limiter{
		MaxKeys: 100000,
		Now:     time.Now,
		limit:   limit,
		window:  window,
		keys:    map[string]*list.Element{},
		order:   list.New(),
	}
}

// Take records an event for key if it has fewer than the limit in the
// window, and says whether it did. When it didn't, retry is how long until
// the oldest event leaves the window. Checking and recording are one step,
// so concurrent takes can't overshoot the limit.
func (l *Limiter) Take(key string) (ok bool, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	cutoff := now.Add(-l.window)
	l.sweep(cutoff)

	el, found := l.keys[key]
	if !found {
		if len(l.keys) >= l.MaxKeys {
			l.remove(l.order.Front())
		}
		el = l.order.PushBack(&entry{key: key})
		l.keys[key] = el
	} else {
		l.order.MoveToBack(el)
	}
	e := el.Value.(*entry)
	e.events = live(e.events, cutoff)
	if len(e.events) >= l.limit {
		return false, e.events[0].Add(l.window).Sub(now)
	}
	e.events = append(e.events, now)
	return true, 0
}

// Return takes back key's newest event, for an attempt that turned out
// not to count.
func (l *Limiter) Return(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	el, found := l.keys[key]
	if !found {
		return
	}
	e := el.Value.(*entry)
	if len(e.events) > 0 {
		e.events = e.events[:len(e.events)-1]
	}
	if len(e.events) == 0 {
		l.remove(el)
	}
}

// sweep drops keys from the front whose events have all left the window.
// The front is the key used longest ago, so this stops at the first key
// still in use: it's cheap per call, and idle keys go eventually.
func (l *Limiter) sweep(cutoff time.Time) {
	for el := l.order.Front(); el != nil; el = l.order.Front() {
		e := el.Value.(*entry)
		if len(e.events) > 0 && e.events[len(e.events)-1].After(cutoff) {
			return
		}
		l.remove(el)
	}
}

func (l *Limiter) remove(el *list.Element) {
	delete(l.keys, el.Value.(*entry).key)
	l.order.Remove(el)
}

// live is events without those at or before cutoff.
func live(events []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(events) && !events[i].After(cutoff) {
		i++
	}
	return events[i:]
}

func (l *Limiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}
