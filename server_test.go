package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestServer wires a server against a fake upstream serving handler.
func newTestServer(t *testing.T, handler http.HandlerFunc) *server {
	t.Helper()

	loc := tehran(t)

	return &server{
		outages: upstream(t, handler),
		reports: newReports(defaultTTL),
		loc:     loc,
		ttl:     defaultTTL,
		logger:  discardLogger(),
		// Pinned an hour before the canned outage, so the fixture stays in the
		// feed no matter when the suite runs.
		now: func() time.Time { return time.Date(2025, 9, 7, 8, 0, 0, 0, loc) },
	}
}

// expire ages every cached report past its TTL so the next request refreshes.
func expire(t *testing.T, s *server) {
	t.Helper()

	s.reports.mu.Lock()
	defer s.reports.mu.Unlock()

	for bill, entry := range s.reports.entries {
		entry.fetched = entry.fetched.Add(-2 * s.reports.ttl)
		s.reports.entries[bill] = entry
	}
}

// okUpstream serves one well-formed outage.
func okUpstream(t *testing.T) http.HandlerFunc {
	t.Helper()

	return respondWith(t, oneOutage())
}

func get(t *testing.T, srv *server, target string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHandleCalendar(t *testing.T) {
	srv := newTestServer(t, okUpstream(t))

	rec := get(t, srv, "/"+testBill)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got, want := rec.Header().Get("Content-Type"), "text/calendar; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.HasPrefix(got, "public, max-age=") {
		t.Errorf("Cache-Control = %q, want a public max-age", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("response carries no ETag")
	}
	if body := rec.Body.String(); !strings.Contains(body, "BEGIN:VEVENT") {
		t.Errorf("body has no event:\n%s", body)
	}
}

func TestHandleCalendarAcceptsICSSuffix(t *testing.T) {
	srv := newTestServer(t, okUpstream(t))

	bare := get(t, srv, "/"+testBill)
	suffixed := get(t, srv, "/"+testBill+".ics")

	if suffixed.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", suffixed.Code, suffixed.Body)
	}
	if bare.Body.String() != suffixed.Body.String() {
		t.Error("the .ics suffix changes the feed")
	}
}

// TestHandleCalendarServesFromCache pins the reason the cache exists: the
// upstream API allows 20 calls an hour for the whole server, so a polling
// subscriber must not reach it on every request.
func TestHandleCalendarServesFromCache(t *testing.T) {
	var calls int
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		okUpstream(t)(w, r)
	})

	for range 5 {
		if rec := get(t, srv, "/"+testBill); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
		}
	}

	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1", calls)
	}
}

func TestHandleCalendarNotModified(t *testing.T) {
	srv := newTestServer(t, okUpstream(t))

	first := get(t, srv, "/"+testBill)
	etag := first.Header().Get("ETag")

	req := httptest.NewRequest(http.MethodGet, "/"+testBill, nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 carries a body: %s", rec.Body)
	}
}

// TestHandleCalendarServesStale pins that a subscriber keeps their calendar
// when a refresh fails. An outage schedule days old beats no schedule.
func TestHandleCalendarServesStale(t *testing.T) {
	var calls int
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			okUpstream(t)(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})

	if rec := get(t, srv, "/"+testBill); rec.Code != http.StatusOK {
		t.Fatalf("first fetch: status = %d, want 200", rec.Code)
	}

	// Age the entry past its TTL so the next request has to refresh.
	expire(t, srv)

	rec := get(t, srv, "/"+testBill)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the stale copy: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "BEGIN:VEVENT") {
		t.Errorf("stale feed has no event:\n%s", rec.Body)
	}
}

func TestHandleCalendarBadRequests(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   int
	}{
		{"non numeric bill", "/abcdefghijklm", http.StatusBadRequest},
		{"too short", "/123456789012", http.StatusBadRequest},
		{"too long", "/12345678901234", http.StatusBadRequest},
		{"suffix only", "/.ics", http.StatusBadRequest},
		{"nested path", "/v1/" + testBill + "/cal.ics", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, func(http.ResponseWriter, *http.Request) {
				t.Error("upstream was called for a request that should have failed validation")
			})

			if rec := get(t, srv, tt.target); rec.Code != tt.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
}

func TestHandleCalendarRejectsNonGET(t *testing.T) {
	srv := newTestServer(t, okUpstream(t))

	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/"+testBill, nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestHandleCalendarUpstreamFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    int
	}{
		{
			name: "quota exceeded",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte("internal upstream detail"))
			},
			want: http.StatusServiceUnavailable,
		},
		{
			name: "bill rejected",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("internal upstream detail"))
			},
			want: http.StatusNotFound,
		},
		{
			name: "upstream down",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				w.Write([]byte("internal upstream detail"))
			},
			want: http.StatusBadGateway,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, tt.handler)

			rec := get(t, srv, "/"+testBill)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			// Upstream wording must never reach the caller.
			if body := rec.Body.String(); strings.Contains(body, "internal upstream detail") {
				t.Errorf("response leaks upstream detail: %s", body)
			}
		})
	}
}

func TestHandleCalendarThrottledSetsRetryAfter(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	rec := get(t, srv, "/"+testBill)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("503 carries no Retry-After")
	}
}

func TestHandleRoot(t *testing.T) {
	srv := newTestServer(t, okUpstream(t))

	rec := get(t, srv, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "barghvim") {
		t.Errorf("root does not describe the service:\n%s", rec.Body)
	}
}

func TestHandleHealth(t *testing.T) {
	srv := newTestServer(t, okUpstream(t))

	rec := get(t, srv, "/healthz")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}

func TestValidateBill(t *testing.T) {
	tests := []struct {
		name    string
		bill    string
		wantErr bool
	}{
		{"typical bill", testBill, false},
		{"all zeros", strings.Repeat("0", billLen), false},
		{"empty", "", true},
		{"one short", strings.Repeat("1", billLen-1), true},
		{"one long", strings.Repeat("1", billLen+1), true},
		{"letters", "12345abc90123", true},
		{"persian digits", "۱۲۳۴۵۶۷۸۹۰۱۲۳", true},
		{"path traversal", "../../etc/pas", true},
		{"whitespace", "123 456789012", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateBill(tt.bill); (err != nil) != tt.wantErr {
				t.Errorf("validateBill(%q) = %v, wantErr %v", tt.bill, err, tt.wantErr)
			}
		})
	}
}

func TestMatchesETag(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{"exact", `"abc"`, true},
		{"weak", `W/"abc"`, true},
		{"in a list", `"other", "abc"`, true},
		{"wildcard", "*", true},
		{"empty", "", false},
		{"different", `"def"`, false},
		{"prefix only", `"ab"`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesETag(tt.header, `"abc"`); got != tt.want {
				t.Errorf("matchesETag(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantAddr  string
		wantTTL   time.Duration
		wantCalls int
		wantErr   bool
	}{
		{"defaults", nil, ":8080", defaultTTL, defaultCallsPerHour, false},
		{"port", map[string]string{"PORT": "9000"}, ":9000", defaultTTL, defaultCallsPerHour, false},
		{
			name:      "addr wins over port",
			env:       map[string]string{"ADDR": "127.0.0.1:1234", "PORT": "9000"},
			wantAddr:  "127.0.0.1:1234",
			wantTTL:   defaultTTL,
			wantCalls: defaultCallsPerHour,
		},
		{
			name:      "cache ttl",
			env:       map[string]string{"CACHE_TTL": "24h"},
			wantAddr:  ":8080",
			wantTTL:   24 * time.Hour,
			wantCalls: defaultCallsPerHour,
		},
		{
			name:      "calls per hour",
			env:       map[string]string{"UPSTREAM_CALLS_PER_HOUR": "5"},
			wantAddr:  ":8080",
			wantTTL:   defaultTTL,
			wantCalls: 5,
		},
		{"ttl too small", map[string]string{"CACHE_TTL": "10s"}, "", 0, 0, true},
		{"ttl too large", map[string]string{"CACHE_TTL": "200h"}, "", 0, 0, true},
		{"ttl not a duration", map[string]string{"CACHE_TTL": "soon"}, "", 0, 0, true},
		{"calls too few", map[string]string{"UPSTREAM_CALLS_PER_HOUR": "0"}, "", 0, 0, true},
		{"calls over the quota", map[string]string{"UPSTREAM_CALLS_PER_HOUR": "21"}, "", 0, 0, true},
		{"bad log level", map[string]string{"LOG_LEVEL": "shout"}, "", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := loadConfig()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("loadConfig() = %+v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() = %v", err)
			}
			if got.Addr != tt.wantAddr {
				t.Errorf("Addr = %q, want %q", got.Addr, tt.wantAddr)
			}
			if got.CacheTTL != tt.wantTTL {
				t.Errorf("CacheTTL = %s, want %s", got.CacheTTL, tt.wantTTL)
			}
			if got.CallsPerHour != tt.wantCalls {
				t.Errorf("CallsPerHour = %d, want %d", got.CallsPerHour, tt.wantCalls)
			}
		})
	}
}

// TestUpcoming pins which outages reach the feed. An outage already over is
// noise in a calendar; one in progress is the single most useful entry there,
// because it carries the estimated reconnection time.
func TestUpcoming(t *testing.T) {
	loc := tehran(t)
	now := time.Date(2025, 9, 7, 12, 0, 0, 0, loc)

	at := func(hour int) time.Time { return time.Date(2025, 9, 7, hour, 0, 0, 0, loc) }

	tests := []struct {
		name string
		in   Outage
		keep bool
	}{
		{"finished hours ago", Outage{Start: at(6), End: at(8)}, false},
		{"ended exactly now", Outage{Start: at(10), End: now}, false},
		{"in progress", Outage{Start: at(11), End: at(13)}, true},
		{"starts later today", Outage{Start: at(14), End: at(16)}, true},
		{"ends in a minute", Outage{Start: at(9), End: now.Add(time.Minute)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upcoming([]Outage{tt.in}, now)
			if (len(got) == 1) != tt.keep {
				t.Errorf("upcoming() kept %d, want keep = %v", len(got), tt.keep)
			}
		})
	}
}

// TestUpcomingLeavesCacheAlone pins that filtering builds a new slice. The
// input belongs to a cached report shared by every request for that bill, so
// filtering in place would strip outages from the cache permanently.
func TestUpcomingLeavesCacheAlone(t *testing.T) {
	loc := tehran(t)
	now := time.Date(2025, 9, 7, 12, 0, 0, 0, loc)

	cached := []Outage{
		{Start: time.Date(2025, 9, 7, 6, 0, 0, 0, loc), End: time.Date(2025, 9, 7, 8, 0, 0, 0, loc)},
		{Start: time.Date(2025, 9, 7, 14, 0, 0, 0, loc), End: time.Date(2025, 9, 7, 16, 0, 0, 0, loc)},
	}

	if got := upcoming(cached, now); len(got) != 1 {
		t.Fatalf("upcoming() kept %d, want 1", len(got))
	}
	if len(cached) != 2 {
		t.Errorf("the cached slice was modified: %+v", cached)
	}
	if !cached[0].Start.Equal(time.Date(2025, 9, 7, 6, 0, 0, 0, loc)) {
		t.Errorf("the cached slice was reordered: %+v", cached)
	}
}

// TestHandleCalendarHidesFinishedOutages checks the filter end to end, on the
// same cached report, so a report fetched before an outage ends stops
// advertising it once it has.
func TestHandleCalendarHidesFinishedOutages(t *testing.T) {
	loc := tehran(t)
	srv := newTestServer(t, okUpstream(t))

	before := get(t, srv, "/"+testBill)
	if !strings.Contains(before.Body.String(), "BEGIN:VEVENT") {
		t.Fatalf("outage missing while it is still ahead:\n%s", before.Body)
	}

	// Same cached report, read after the outage has finished.
	srv.now = func() time.Time { return time.Date(2025, 9, 7, 18, 0, 0, 0, loc) }

	after := get(t, srv, "/"+testBill)
	if after.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", after.Code, after.Body)
	}
	if strings.Contains(after.Body.String(), "BEGIN:VEVENT") {
		t.Errorf("a finished outage is still in the feed:\n%s", after.Body)
	}
	if after.Header().Get("ETag") == before.Header().Get("ETag") {
		t.Error("the feed changed but the ETag did not")
	}
}
