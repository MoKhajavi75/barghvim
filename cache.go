package main

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// defaultCallsPerHour is the upstream allowance. The API answers
	//
	//	429 API calls quota exceeded! maximum admitted 20 per 1h.
	//
	// and counts per source IP, not per session, so the whole service shares
	// one allowance. Two are held back so a burst of cold lookups cannot spend
	// the hour's last call and leave nothing for a retry.
	defaultCallsPerHour = 18

	// defaultTTL is how long a fetched report is reused. Outages are announced
	// one to seven days ahead, so this trades no useful warning for a large
	// cut in upstream calls.
	defaultTTL = 6 * time.Hour

	// maxStale is how long a cached report keeps being served once refreshing
	// it fails. A stale calendar beats an empty one.
	maxStale = 7 * 24 * time.Hour

	// loadTimeout bounds one detached upstream load, including the extra round
	// trip a bot-check solve costs.
	loadTimeout = 45 * time.Second

	// maxCacheTTL and maxCallsPerHour bound the configurable knobs.
	maxCacheTTL     = 7 * 24 * time.Hour
	maxCallsPerHour = 20

	// maxCacheEntries bounds memory. Entries are cheap, but the bill number
	// comes from the URL, so the key space is attacker-controlled.
	maxCacheEntries = 4096
)

// budget is a token bucket over upstream calls. It refills continuously
// rather than in steps, so a spent allowance recovers gradually instead of
// releasing a thundering herd on the hour.
type budget struct {
	mu       sync.Mutex
	capacity float64
	perSec   float64
	tokens   float64
	last     time.Time
	now      func() time.Time
}

func newBudget(capacity int, window time.Duration) *budget {
	b := &budget{
		capacity: float64(capacity),
		perSec:   float64(capacity) / window.Seconds(),
		tokens:   float64(capacity),
		now:      time.Now,
	}
	b.last = b.now()
	return b
}

// allow spends one call if the budget has one left.
func (b *budget) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(b.capacity, b.tokens+elapsed*b.perSec)
		b.last = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// drain empties the budget after upstream says the quota is spent. The local
// allowance is only an estimate of the remote counter, and the two drift —
// restarts forget it, and any other client on the same IP spends from the same
// quota. Without this, every request keeps calling an API that is already
// answering 429. Refill then paces the retries at one per capacity-th of the
// window, which is the fastest cadence the allowance could ever support.
func (b *budget) drain() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.tokens = 0
	b.last = b.now()
}

// reports caches one Report per bill number and collapses concurrent lookups
// of the same bill into a single upstream call.
type reports struct {
	ttl   time.Duration
	group singleflight.Group

	mu      sync.Mutex
	entries map[string]cached

	now func() time.Time
}

type cached struct {
	report  Report
	fetched time.Time
}

func newReports(ttl time.Duration) *reports {
	return &reports{
		ttl:     ttl,
		entries: make(map[string]cached),
		now:     time.Now,
	}
}

// fetcher loads a report for one bill number from upstream.
type fetcher func(ctx context.Context, bill string) (Report, error)

// get returns a cached report if it is still fresh, and otherwise refreshes
// it. When the refresh fails but a stale copy is on hand, the stale copy is
// returned with a nil error and stale set — losing the feed entirely is worse
// than serving yesterday's copy of a schedule announced days ahead.
func (r *reports) get(ctx context.Context, bill string, load fetcher) (rep Report, stale bool, err error) {
	if entry, ok := r.lookup(bill); ok && r.now().Sub(entry.fetched) < r.ttl {
		return entry.report, false, nil
	}

	ch := r.group.DoChan(bill, func() (any, error) {
		// A concurrent caller may have refreshed this bill while we queued.
		if entry, ok := r.lookup(bill); ok && r.now().Sub(entry.fetched) < r.ttl {
			return entry.report, nil
		}

		// The load is detached from the caller that triggered it. One
		// subscriber hanging up must not cancel a lookup the others are
		// waiting on, and against a 20-an-hour allowance a call already in
		// flight is worth finishing and caching either way.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()

		fresh, err := load(loadCtx, bill)
		if err != nil {
			return nil, err
		}
		r.store(bill, fresh)
		return fresh, nil
	})

	select {
	case res := <-ch:
		if res.Err == nil {
			return res.Val.(Report), false, nil
		}
		err = res.Err
	case <-ctx.Done():
		err = ctx.Err()
	}

	if entry, ok := r.lookup(bill); ok && r.now().Sub(entry.fetched) < maxStale {
		return entry.report, true, nil
	}
	return Report{}, false, err
}

func (r *reports) lookup(bill string) (cached, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.entries[bill]
	return entry, ok
}

func (r *reports) store(bill string, rep Report) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.entries) >= maxCacheEntries {
		if _, replacing := r.entries[bill]; !replacing {
			r.evictOldestLocked()
		}
	}
	r.entries[bill] = cached{report: rep, fetched: r.now()}
}

func (r *reports) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for k, v := range r.entries {
		if oldestKey == "" || v.fetched.Before(oldest) {
			oldestKey, oldest = k, v.fetched
		}
	}
	delete(r.entries, oldestKey)
}
