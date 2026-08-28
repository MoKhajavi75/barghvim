package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// billLen is the length of an Iranian electricity bill number. Enforcing it
// here keeps junk paths from spending the upstream call budget.
const billLen = 13

// calendarSuffix is accepted on the end of a bill number so the feed URL can
// look like a file to clients that sniff the extension.
const calendarSuffix = ".ics"

// staleMaxAge is what a stale feed is cached for. A stale copy is served when
// a refresh failed, so clients should come back sooner than the full TTL.
const staleMaxAge = 15 * time.Minute

const usage = "barghvim — planned power outages as a calendar feed\n\n" +
	"Subscribe to https://<host>/<13-digit bill number>.ics\n\n" +
	repoURL + "\n"

type server struct {
	outages *Client
	reports *reports
	loc     *time.Location
	ttl     time.Duration
	logger  *slog.Logger

	// now is overridden by tests that need a fixed clock.
	now func() time.Time
}

func (s *server) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /{bill}", s.handleCalendar)

	return withRequestLog(s.logger, mux)
}

func (s *server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(usage))
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok\n"))
}

func (s *server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	bill := strings.TrimSuffix(r.PathValue("bill"), calendarSuffix)
	if err := validateBill(bill); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rep, stale, err := s.reports.get(r.Context(), bill, s.outages.Fetch)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// A report is cached for hours, so what counts as "already over" is
	// decided per request rather than at fetch time.
	rep.Outages = upcoming(rep.Outages, s.clock().In(s.loc))

	feed, err := buildICS(rep, s.loc)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// The feed is byte-identical between refreshes of unchanged data, so an
	// ETag lets a polling client answer itself with a 304. Upstream allows
	// only 20 calls an hour, which makes every avoided poll worth having.
	maxAge := s.ttl
	if stale {
		s.logger.Warn("serving stale report", "route", r.Pattern)
		maxAge = min(maxAge, staleMaxAge)
	}

	etag := feedETag(feed)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(maxAge.Seconds())))
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(feed)))
	w.Write(feed)
}

// fail logs the full error and returns a message that reveals nothing about
// the upstream API to the caller.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return // the client hung up; nothing left to write to
	}

	status, msg := http.StatusInternalServerError, "internal error"
	switch {
	case errors.Is(err, ErrBadBill):
		status, msg = http.StatusNotFound, "no outage data for that bill number"
	case errors.Is(err, ErrThrottled):
		status, msg = http.StatusServiceUnavailable, "upstream call budget is spent; try again later"
	case errors.Is(err, ErrUpstream), errors.Is(err, context.DeadlineExceeded):
		status, msg = http.StatusBadGateway, "upstream API is unavailable"
	}

	if status == http.StatusServiceUnavailable {
		// Nothing is gained by polling before the budget refills.
		w.Header().Set("Retry-After", strconv.Itoa(int((10 * time.Minute).Seconds())))
	}

	s.logger.Error("request failed", "route", r.Pattern, "status", status, "err", err)
	http.Error(w, msg, status)
}

// upcoming drops outages that are already over. An outage in progress is kept
// — its end is still ahead — so a subscriber checking mid-blackout still sees
// when the power is due back. The result is a new slice: the input belongs to
// a cached report that other requests share.
func upcoming(outages []Outage, now time.Time) []Outage {
	kept := make([]Outage, 0, len(outages))
	for _, o := range outages {
		if o.End.After(now) {
			kept = append(kept, o)
		}
	}
	return kept
}

func validateBill(bill string) error {
	if bill == "" {
		return errors.New("missing bill number")
	}
	if len(bill) != billLen {
		return errors.New("bill number must be " + strconv.Itoa(billLen) + " digits")
	}
	for _, c := range bill {
		if c < '0' || c > '9' {
			return errors.New("bill number must contain digits only")
		}
	}
	return nil
}

// feedETag derives a strong validator from the rendered feed.
func feedETag(feed []byte) string {
	sum := sha256.Sum256(feed)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// matchesETag reports whether an If-None-Match header covers etag. Clients
// may send a list, and a proxy may have weakened the tag on the way out.
func matchesETag(header, etag string) bool {
	for candidate := range strings.SplitSeq(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// withRequestLog records the matched route rather than the raw URL, keeping
// the bill number out of the logs.
func withRequestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		logger.Info("request",
			"method", r.Method,
			"route", cmp.Or(r.Pattern, "unmatched"),
			"status", rec.status,
			"duration", time.Since(start).String(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
