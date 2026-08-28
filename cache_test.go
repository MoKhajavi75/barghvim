package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// clock is a manually advanced time source, so cache and budget tests can
// cover hours of behaviour without sleeping.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2025, 9, 7, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestBudgetSpendsAndRefills(t *testing.T) {
	clk := newClock()
	b := newBudget(3, time.Hour)
	b.now, b.last = clk.now, clk.now()

	for i := range 3 {
		if !b.allow() {
			t.Fatalf("call %d was refused with budget to spare", i+1)
		}
	}
	if b.allow() {
		t.Error("budget allowed a fourth call in the same instant")
	}

	// Three an hour is one every twenty minutes.
	clk.advance(20 * time.Minute)
	if !b.allow() {
		t.Error("budget did not refill after a third of the window")
	}
	if b.allow() {
		t.Error("budget refilled by more than one call")
	}
}

func TestBudgetDoesNotOverfill(t *testing.T) {
	clk := newClock()
	b := newBudget(2, time.Hour)
	b.now, b.last = clk.now, clk.now()

	// Idling for a day must not bank a day's worth of calls.
	clk.advance(24 * time.Hour)

	for i := range 2 {
		if !b.allow() {
			t.Fatalf("call %d was refused at full capacity", i+1)
		}
	}
	if b.allow() {
		t.Error("budget banked more than its capacity while idle")
	}
}

func TestReportsServesFreshFromCache(t *testing.T) {
	clk := newClock()
	r := newReports(time.Hour)
	r.now = clk.now

	var calls int
	load := func(context.Context, string) (Report, error) {
		calls++
		return Report{Bill: testBill}, nil
	}

	for range 5 {
		if _, stale, err := r.get(t.Context(), testBill, load); err != nil || stale {
			t.Fatalf("get() = stale %v, err %v", stale, err)
		}
	}
	if calls != 1 {
		t.Errorf("loads = %d, want 1", calls)
	}

	clk.advance(2 * time.Hour)
	if _, _, err := r.get(t.Context(), testBill, load); err != nil {
		t.Fatalf("get() = %v", err)
	}
	if calls != 2 {
		t.Errorf("loads after expiry = %d, want 2", calls)
	}
}

func TestReportsServesStaleOnFailure(t *testing.T) {
	clk := newClock()
	r := newReports(time.Hour)
	r.now = clk.now

	fail := false
	load := func(context.Context, string) (Report, error) {
		if fail {
			return Report{}, ErrUpstream
		}
		return Report{Bill: testBill, Address: "somewhere"}, nil
	}

	if _, _, err := r.get(t.Context(), testBill, load); err != nil {
		t.Fatalf("first get() = %v", err)
	}

	clk.advance(2 * time.Hour)
	fail = true

	got, stale, err := r.get(t.Context(), testBill, load)
	if err != nil {
		t.Fatalf("get() = %v, want the stale copy", err)
	}
	if !stale {
		t.Error("stale copy was not reported as stale")
	}
	if got.Address != "somewhere" {
		t.Errorf("get() = %+v, want the cached report", got)
	}
}

func TestReportsGivesUpOnAncientCopies(t *testing.T) {
	clk := newClock()
	r := newReports(time.Hour)
	r.now = clk.now

	fail := false
	load := func(context.Context, string) (Report, error) {
		if fail {
			return Report{}, ErrUpstream
		}
		return Report{Bill: testBill}, nil
	}

	if _, _, err := r.get(t.Context(), testBill, load); err != nil {
		t.Fatalf("first get() = %v", err)
	}

	clk.advance(maxStale + time.Hour)
	fail = true

	if _, _, err := r.get(t.Context(), testBill, load); !errors.Is(err, ErrUpstream) {
		t.Errorf("get() = %v, want %v", err, ErrUpstream)
	}
}

func TestReportsPropagatesFirstFailure(t *testing.T) {
	r := newReports(time.Hour)

	load := func(context.Context, string) (Report, error) {
		return Report{}, ErrThrottled
	}

	if _, _, err := r.get(t.Context(), testBill, load); !errors.Is(err, ErrThrottled) {
		t.Errorf("get() = %v, want %v", err, ErrThrottled)
	}
}

// TestReportsCollapsesConcurrentLoads pins that a burst of subscribers hitting
// a cold bill costs one upstream call, not one each. Upstream allows twenty an
// hour for the whole service.
func TestReportsCollapsesConcurrentLoads(t *testing.T) {
	r := newReports(time.Hour)

	var (
		mu    sync.Mutex
		calls int
	)
	release := make(chan struct{})
	load := func(context.Context, string) (Report, error) {
		mu.Lock()
		calls++
		mu.Unlock()

		<-release
		return Report{Bill: testBill}, nil
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, _, err := r.get(t.Context(), testBill, load); err != nil {
				t.Errorf("get() = %v", err)
			}
		})
	}

	// Give the goroutines time to pile onto the same key before releasing.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("loads = %d, want 1", calls)
	}
}

func TestReportsEvictsOldest(t *testing.T) {
	clk := newClock()
	r := newReports(time.Hour)
	r.now = clk.now

	load := func(_ context.Context, bill string) (Report, error) {
		return Report{Bill: bill}, nil
	}

	// Fill past the cap, one distinct bill per minute.
	for i := range maxCacheEntries + 10 {
		bill := "bill" + strconv.Itoa(i)
		if _, _, err := r.get(t.Context(), bill, load); err != nil {
			t.Fatalf("get(%q) = %v", bill, err)
		}
		clk.advance(time.Minute)
	}

	r.mu.Lock()
	size := len(r.entries)
	_, keptOldest := r.entries["bill0"]
	r.mu.Unlock()

	if size > maxCacheEntries {
		t.Errorf("cache holds %d entries, over the cap of %d", size, maxCacheEntries)
	}
	if keptOldest {
		t.Error("the oldest entry survived eviction")
	}
}

// TestBudgetDrainsOnUpstreamRefusal pins that the local allowance yields to
// upstream's own counter. The two drift — a restart forgets ours, and anything
// else on the same IP spends from the same quota — so a 429 has to stop the
// calls that would otherwise keep arriving with local budget to spare.
func TestBudgetDrainsOnUpstreamRefusal(t *testing.T) {
	clk := newClock()
	b := newBudget(6, time.Hour)
	b.now, b.last = clk.now, clk.now()

	if !b.allow() {
		t.Fatal("first call was refused at full capacity")
	}
	b.drain()

	if b.allow() {
		t.Error("budget allowed a call straight after being drained")
	}

	// Six an hour refills one every ten minutes.
	clk.advance(10 * time.Minute)
	if !b.allow() {
		t.Error("budget did not refill after a sixth of the window")
	}
}

// TestReportsFinishesLoadAfterCallerHangsUp pins that a subscriber closing the
// connection does not throw away the upstream call made on their behalf. The
// call is already spent against a 20-an-hour allowance, so the result belongs
// in the cache whether or not anyone is still waiting for it.
func TestReportsFinishesLoadAfterCallerHangsUp(t *testing.T) {
	r := newReports(time.Hour)

	started := make(chan struct{})
	load := func(ctx context.Context, bill string) (Report, error) {
		close(started)
		time.Sleep(50 * time.Millisecond)
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		return Report{Bill: bill, Address: "somewhere"}, nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()

	if _, _, err := r.get(ctx, testBill, load); !errors.Is(err, context.Canceled) {
		t.Fatalf("get() = %v, want context.Canceled", err)
	}

	// The detached load keeps going and stores its result.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if entry, ok := r.lookup(testBill); ok {
			if entry.report.Address != "somewhere" {
				t.Fatalf("cached %+v, want the completed load", entry.report)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the abandoned load never populated the cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
