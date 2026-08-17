package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestServer wires a server against a fake upstream serving handler.
func newTestServer(t *testing.T, handler http.HandlerFunc) http.Handler {
	t.Helper()

	return (&server{
		outages: upstream(t, handler),
		loc:     tehran(t),
		window:  defaultWindow,
		logger:  discardLogger(),
	}).routes()
}

// okUpstream serves one well-formed outage.
func okUpstream(t *testing.T) http.HandlerFunc {
	t.Helper()

	return respondWith(t, reportResponse{
		Status: 200,
		Data:   []reportItem{{Date: "1404/06/16", Start: "09:00", Stop: "11:00"}},
	})
}

func TestHandleCalendar(t *testing.T) {
	srv := newTestServer(t, okUpstream(t))

	req := httptest.NewRequest(http.MethodGet, "/v1/1234567890/cal.ics?token=tok", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got, want := rec.Header().Get("Content-Type"), "text/calendar; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}
	if body := rec.Body.String(); !strings.Contains(body, "BEGIN:VEVENT") {
		t.Errorf("body has no event:\n%s", body)
	}
}

func TestHandleCalendarBadRequests(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   int
	}{
		{"missing token", "/v1/1234567890/cal.ics", http.StatusBadRequest},
		{"empty token", "/v1/1234567890/cal.ics?token=", http.StatusBadRequest},
		{"non numeric bill", "/v1/abc/cal.ics?token=tok", http.StatusBadRequest},
		{"oversized bill", "/v1/" + strings.Repeat("1", maxBillLen+1) + "/cal.ics?token=tok", http.StatusBadRequest},
		{"unknown path", "/v1/1234567890/other.ics?token=tok", http.StatusNotFound},
		{"root", "/", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("upstream was called for a request that should have failed validation")
			})

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
}

func TestHandleCalendarRejectsNonGET(t *testing.T) {
	srv := newTestServer(t, okUpstream(t))

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/1234567890/cal.ics?token=tok", nil))

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
			name:    "expired token",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
			want:    http.StatusUnauthorized,
		},
		{
			name:    "upstream down",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
			want:    http.StatusBadGateway,
		},
		{
			name: "upstream application error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				json.NewEncoder(w).Encode(reportResponse{Status: 500, Message: "internal upstream detail"})
			},
			want: http.StatusBadGateway,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, tt.handler)

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/1234567890/cal.ics?token=tok", nil))

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

func TestHandleHealth(t *testing.T) {
	srv := newTestServer(t, okUpstream(t))

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

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
		{"typical bill", "1234567890", false},
		{"single digit", "7", false},
		{"max length", strings.Repeat("1", maxBillLen), false},
		{"empty", "", true},
		{"too long", strings.Repeat("1", maxBillLen+1), true},
		{"letters", "12345abc", true},
		{"persian digits", "۱۲۳۴", true},
		{"path traversal", "../../etc", true},
		{"whitespace", "123 456", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateBill(tt.bill); (err != nil) != tt.wantErr {
				t.Errorf("validateBill(%q) = %v, wantErr %v", tt.bill, err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		wantAddr string
		wantWin  time.Duration
		wantErr  bool
	}{
		{"defaults", nil, ":8080", defaultWindow, false},
		{"port", map[string]string{"PORT": "9000"}, ":9000", defaultWindow, false},
		{"addr wins over port", map[string]string{"ADDR": "127.0.0.1:1234", "PORT": "9000"}, "127.0.0.1:1234", defaultWindow, false},
		{"window", map[string]string{"OUTAGE_WINDOW_DAYS": "14"}, ":8080", 14 * 24 * time.Hour, false},
		{"window too small", map[string]string{"OUTAGE_WINDOW_DAYS": "0"}, "", 0, true},
		{"window too large", map[string]string{"OUTAGE_WINDOW_DAYS": "61"}, "", 0, true},
		{"window not a number", map[string]string{"OUTAGE_WINDOW_DAYS": "many"}, "", 0, true},
		{"bad log level", map[string]string{"LOG_LEVEL": "shout"}, "", 0, true},
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
			if got.Window != tt.wantWin {
				t.Errorf("Window = %s, want %s", got.Window, tt.wantWin)
			}
		})
	}
}
