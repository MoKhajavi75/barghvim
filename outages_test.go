package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// discardLogger keeps skipped-row warnings out of the test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// upstream starts a fake برق من API that serves handler, and returns a Client
// pointed at it.
func upstream(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return NewClient(tehran(t), WithEndpoint(srv.URL), WithLogger(discardLogger()), WithHTTPClient(srv.Client()))
}

// respondWith serves a canned report body with an HTTP 200.
func respondWith(t *testing.T, body reportResponse) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encoding fake response: %v", err)
		}
	}
}

func TestFetchRequestShape(t *testing.T) {
	loc := tehran(t)

	var (
		gotAuth   string
		gotMethod string
		gotBody   reportRequest
	)

	client := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		json.NewEncoder(w).Encode(reportResponse{Status: 200})
	})

	from := time.Date(2025, 9, 7, 12, 0, 0, 0, loc)
	if _, err := client.Fetch(t.Context(), "tok", "1234567890", from, from.Add(7*24*time.Hour)); err != nil {
		t.Fatalf("Fetch() = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "Bearer tok"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}

	// The window has to reach the API as Jalali dates.
	want := reportRequest{BillID: "1234567890", From: "1404/06/16", To: "1404/06/23"}
	if diff := cmp.Diff(want, gotBody); diff != "" {
		t.Errorf("request body mismatch (-want +got):\n%s", diff)
	}
}

func TestFetchParsesOutages(t *testing.T) {
	loc := tehran(t)

	client := upstream(t, respondWith(t, reportResponse{
		Status: 200,
		Data: []reportItem{
			// Deliberately out of order: the feed must come back sorted.
			{Date: "1404/06/17", Start: "14:00", Stop: "16:00"},
			{Date: "1404/06/16", Start: "09:00", Stop: "11:00"},
		},
	}))

	got, err := client.Fetch(t.Context(), "tok", "1234567890", time.Now(), time.Now())
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}

	want := []Outage{
		{
			Start: time.Date(2025, 9, 7, 9, 0, 0, 0, loc),
			End:   time.Date(2025, 9, 7, 11, 0, 0, 0, loc),
		},
		{
			Start: time.Date(2025, 9, 8, 14, 0, 0, 0, loc),
			End:   time.Date(2025, 9, 8, 16, 0, 0, 0, loc),
		},
	}
	if diff := cmp.Diff(want, got, cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })); diff != "" {
		t.Errorf("outage mismatch (-want +got):\n%s", diff)
	}
}

func TestFetchOvernightOutage(t *testing.T) {
	loc := tehran(t)

	// A row carries one date and two clock times, so an outage crossing
	// midnight has to be rolled onto the following day.
	client := upstream(t, respondWith(t, reportResponse{
		Status: 200,
		Data:   []reportItem{{Date: "1404/06/16", Start: "22:00", Stop: "02:00"}},
	}))

	got, err := client.Fetch(t.Context(), "tok", "1234567890", time.Now(), time.Now())
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d outages, want 1", len(got))
	}

	wantEnd := time.Date(2025, 9, 8, 2, 0, 0, 0, loc)
	if !got[0].End.Equal(wantEnd) {
		t.Errorf("end = %s, want %s", got[0].End.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
	}
}

func TestFetchSkipsUnusableRows(t *testing.T) {
	client := upstream(t, respondWith(t, reportResponse{
		Status: 200,
		Data: []reportItem{
			{Date: "not-a-date", Start: "09:00", Stop: "11:00"},
			{Date: "1404/06/16", Start: "09:00", Stop: "bad"},
			{Date: "1404/06/16", Start: "09:00", Stop: "09:00"}, // zero length
			{Date: "1404/06/16", Start: "09:00", Stop: "11:00"}, // the only good row
		},
	}))

	// One bad row must not cost the subscriber their whole calendar.
	got, err := client.Fetch(t.Context(), "tok", "1234567890", time.Now(), time.Now())
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d outages, want 1: %+v", len(got), got)
	}
}

func TestFetchErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{
			name:    "http unauthorized",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
			want:    ErrUnauthorized,
		},
		{
			name:    "http forbidden",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) },
			want:    ErrUnauthorized,
		},
		{
			name:    "http server error",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			want:    ErrUpstream,
		},
		{
			name: "unauthorized in the body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				json.NewEncoder(w).Encode(reportResponse{Status: 401, Message: "token expired"})
			},
			want: ErrUnauthorized,
		},
		{
			name: "application error in the body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				json.NewEncoder(w).Encode(reportResponse{Status: 500, Message: "boom"})
			},
			want: ErrUpstream,
		},
		{
			name:    "malformed json",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("{{{")) },
			want:    ErrUpstream,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := upstream(t, tt.handler)

			_, err := client.Fetch(t.Context(), "tok", "1234567890", time.Now(), time.Now())
			if !errors.Is(err, tt.want) {
				t.Errorf("Fetch() = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestFetchUnreachableUpstream(t *testing.T) {
	client := NewClient(tehran(t),
		WithEndpoint("http://127.0.0.1:1"),
		WithLogger(discardLogger()),
		WithHTTPClient(&http.Client{Timeout: time.Second}),
	)

	_, err := client.Fetch(t.Context(), "tok", "1234567890", time.Now(), time.Now())
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("Fetch() = %v, want %v", err, ErrUpstream)
	}
}

func TestFetchHonorsContextCancellation(t *testing.T) {
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := client.Fetch(ctx, "tok", "1234567890", time.Now(), time.Now()); !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch() = %v, want context.Canceled", err)
	}
}
