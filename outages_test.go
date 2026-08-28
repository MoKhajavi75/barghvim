package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// testBill is a syntactically valid but fictitious bill number.
const testBill = "1234567890123"

// discardLogger keeps skipped-row warnings out of the test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// upstream starts a fake outage API that serves handler, and returns a Client
// pointed at it.
func upstream(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := srv.Client()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New() = %v", err)
	}
	client.Jar = jar

	return NewClient(tehran(t), WithEndpoint(srv.URL), WithLogger(discardLogger()), WithHTTPClient(client))
}

// respondWith serves a canned bill lookup with an HTTP 200.
func respondWith(t *testing.T, bills ...billResponse) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(bills); err != nil {
			t.Errorf("encoding fake response: %v", err)
		}
	}
}

// oneOutage is a well-formed bill lookup with a single two-hour outage.
func oneOutage() billResponse {
	bill := testBill
	address := "خیابان نمونه، پلاک ۱"
	lat, lng := 35.700000, 51.400000

	return billResponse{
		BillID:    &bill,
		Latitude:  &lat,
		Longitude: &lng,
		Address:   &address,
		Outages: []outageItem{{
			Number:    251035,
			Reason:    "مدیریت انرژی",
			Start:     "1404/06/16 09:00",
			Reconnect: "1404/06/16 11:00",
		}},
	}
}

// challengePage is the real interstitial the bot check serves, captured from
// production. Keeping the genuine article as a fixture means a change to the
// generator shows up here rather than in a subscriber's empty calendar.
func challengePage(t *testing.T) []byte {
	t.Helper()

	page, err := os.ReadFile("testdata/challenge.html")
	if err != nil {
		t.Fatalf("reading challenge fixture: %v", err)
	}
	return page
}

func TestFetchRequestShape(t *testing.T) {
	var (
		gotMethod string
		gotType   string
		gotBill   string
	)

	client := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotType = r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing request form: %v", err)
		}
		gotBill = r.PostForm.Get("BillId")

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})

	if _, err := client.Fetch(t.Context(), testBill); err != nil {
		t.Fatalf("Fetch() = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "application/x-www-form-urlencoded"; gotType != want {
		t.Errorf("Content-Type = %q, want %q", gotType, want)
	}
	if gotBill != testBill {
		t.Errorf("BillId = %q, want %q", gotBill, testBill)
	}
}

func TestFetchParsesReport(t *testing.T) {
	loc := tehran(t)

	bill := oneOutage()
	// Deliberately out of order: the feed must come back sorted.
	bill.Outages = append([]outageItem{{
		Number:    251036,
		Reason:    "تعمیرات",
		Start:     "1404/06/17 14:00",
		Reconnect: "1404/06/17 16:00",
	}}, bill.Outages...)

	client := upstream(t, respondWith(t, bill))

	got, err := client.Fetch(t.Context(), testBill)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}

	want := Report{
		Bill:      testBill,
		Address:   "خیابان نمونه، پلاک ۱",
		Latitude:  35.700000,
		Longitude: 51.400000,
		HasCoords: true,
		Outages: []Outage{
			{
				Start:  time.Date(2025, 9, 7, 9, 0, 0, 0, loc),
				End:    time.Date(2025, 9, 7, 11, 0, 0, 0, loc),
				Reason: "مدیریت انرژی",
			},
			{
				Start:  time.Date(2025, 9, 8, 14, 0, 0, 0, loc),
				End:    time.Date(2025, 9, 8, 16, 0, 0, 0, loc),
				Reason: "تعمیرات",
			},
		},
	}
	if diff := cmp.Diff(want, got, cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })); diff != "" {
		t.Errorf("report mismatch (-want +got):\n%s", diff)
	}
}

func TestFetchOvernightOutage(t *testing.T) {
	loc := tehran(t)

	// Both stamps carry their own date, so an outage crossing midnight needs
	// no repair — unlike the old report format, which dated only the start.
	bill := oneOutage()
	bill.Outages = []outageItem{{
		Start:     "1404/06/16 22:00",
		Reconnect: "1404/06/17 02:00",
	}}

	client := upstream(t, respondWith(t, bill))

	got, err := client.Fetch(t.Context(), testBill)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if len(got.Outages) != 1 {
		t.Fatalf("got %d outages, want 1", len(got.Outages))
	}

	wantEnd := time.Date(2025, 9, 8, 2, 0, 0, 0, loc)
	if !got.Outages[0].End.Equal(wantEnd) {
		t.Errorf("end = %s, want %s", got.Outages[0].End.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
	}
}

func TestFetchSkipsUnusableRows(t *testing.T) {
	bill := oneOutage()
	bill.Outages = []outageItem{
		{Start: "not-a-stamp", Reconnect: "1404/06/16 11:00"},
		{Start: "1404/06/16 09:00", Reconnect: "bad"},
		{Start: "1404/06/16 09:00", Reconnect: "1404/06/16 09:00"}, // zero length
		{Start: "1404/06/16 11:00", Reconnect: "1404/06/16 09:00"}, // ends before it starts
		{Start: "1404/06/16 09:00", Reconnect: "1404/06/16 11:00"}, // the only good row
	}

	client := upstream(t, respondWith(t, bill))

	// One bad row must not cost the subscriber their whole calendar.
	got, err := client.Fetch(t.Context(), testBill)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if len(got.Outages) != 1 {
		t.Fatalf("got %d outages, want 1: %+v", len(got.Outages), got.Outages)
	}
}

func TestFetchUnknownBill(t *testing.T) {
	// An unknown bill number is not an error upstream: it answers 200 with a
	// fully null entry and no outages.
	client := upstream(t, respondWith(t, billResponse{Outages: []outageItem{}}))

	got, err := client.Fetch(t.Context(), testBill)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if len(got.Outages) != 0 || got.HasCoords || got.Address != "" {
		t.Errorf("Fetch() = %+v, want an empty report", got)
	}
}

func TestFetchEmptyArray(t *testing.T) {
	client := upstream(t, respondWith(t))

	got, err := client.Fetch(t.Context(), testBill)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if got.Bill != testBill || len(got.Outages) != 0 {
		t.Errorf("Fetch() = %+v, want an empty report for %s", got, testBill)
	}
}

func TestFetchSolvesChallenge(t *testing.T) {
	page := challengePage(t)

	var (
		calls      int
		sentSecond []*http.Cookie
	)

	client := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// The bot check answers 200 with HTML, not an error status.
			w.Header().Set("Content-Type", "text/html")
			w.Write(page)
			return
		}
		sentSecond = r.Cookies()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]billResponse{oneOutage()})
	})

	got, err := client.Fetch(t.Context(), testBill)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if calls != 2 {
		t.Errorf("upstream calls = %d, want 2", calls)
	}
	if len(got.Outages) != 1 {
		t.Errorf("got %d outages, want 1", len(got.Outages))
	}

	names := make(map[string]string, len(sentSecond))
	for _, c := range sentSecond {
		names[c.Name] = c.Value
	}
	for _, want := range []string{"__arcsjs", "__arcsjsc"} {
		if names[want] == "" {
			t.Errorf("retry did not carry the %s cookie, sent %v", want, names)
		}
	}
}

func TestFetchReusesChallengeCookies(t *testing.T) {
	page := challengePage(t)

	var calls int
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if len(r.Cookies()) == 0 {
			w.Header().Set("Content-Type", "text/html")
			w.Write(page)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]billResponse{oneOutage()})
	})

	for range 3 {
		if _, err := client.Fetch(t.Context(), testBill); err != nil {
			t.Fatalf("Fetch() = %v", err)
		}
	}

	// One challenge round plus three lookups. Solving it again per fetch
	// would double the cost against a 20-call hourly allowance.
	if want := 4; calls != want {
		t.Errorf("upstream calls = %d, want %d", calls, want)
	}
}

func TestFetchChallengeNeverClears(t *testing.T) {
	page := challengePage(t)

	client := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write(page)
	})

	_, err := client.Fetch(t.Context(), testBill)
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("Fetch() = %v, want %v", err, ErrUpstream)
	}
}

func TestFetchErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{
			name: "quota exceeded",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte("API calls quota exceeded! maximum admitted 20 per 1h."))
			},
			want: ErrThrottled,
		},
		{
			name:    "malformed bill",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) },
			want:    ErrBadBill,
		},
		{
			name:    "server error",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			want:    ErrUpstream,
		},
		{
			name: "malformed json",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte("{{{"))
			},
			want: ErrUpstream,
		},
		{
			name: "html that is not a challenge",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.Write([]byte("<html><body>maintenance</body></html>"))
			},
			want: ErrUpstream,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := upstream(t, tt.handler)

			if _, err := client.Fetch(t.Context(), testBill); !errors.Is(err, tt.want) {
				t.Errorf("Fetch() = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestFetchRespectsBudget(t *testing.T) {
	var calls int
	client := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]billResponse{oneOutage()})
	})
	WithBudget(newBudget(1, time.Hour))(client)

	if _, err := client.Fetch(t.Context(), testBill); err != nil {
		t.Fatalf("first Fetch() = %v", err)
	}
	if _, err := client.Fetch(t.Context(), testBill); !errors.Is(err, ErrThrottled) {
		t.Errorf("second Fetch() = %v, want %v", err, ErrThrottled)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1", calls)
	}
}

func TestFetchUnreachableUpstream(t *testing.T) {
	client := NewClient(tehran(t),
		WithEndpoint("http://127.0.0.1:1"),
		WithLogger(discardLogger()),
		WithHTTPClient(&http.Client{Timeout: time.Second}),
	)

	if _, err := client.Fetch(t.Context(), testBill); !errors.Is(err, ErrUpstream) {
		t.Errorf("Fetch() = %v, want %v", err, ErrUpstream)
	}
}

func TestFetchHonorsContextCancellation(t *testing.T) {
	client := upstream(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := client.Fetch(ctx, testBill); !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch() = %v, want context.Canceled", err)
	}
}

// TestFetchStopsCallingAfterUpstream429 pins that one refusal takes the rest of
// the local allowance with it. Otherwise a cold cache means every request keeps
// hammering an API that has already said no.
func TestFetchStopsCallingAfterUpstream429(t *testing.T) {
	var calls int
	client := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("API calls quota exceeded! maximum admitted 20 per 1h."))
	})
	WithBudget(newBudget(10, time.Hour))(client)

	for range 5 {
		if _, err := client.Fetch(t.Context(), testBill); !errors.Is(err, ErrThrottled) {
			t.Fatalf("Fetch() = %v, want %v", err, ErrThrottled)
		}
	}

	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1", calls)
	}
}
