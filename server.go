package main

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// maxBillLen bounds the bill number before it reaches the upstream API.
const maxBillLen = 20

type server struct {
	outages *Client
	loc     *time.Location
	window  time.Duration
	logger  *slog.Logger
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/{bill}/cal.ics", s.handleCalendar)
	mux.HandleFunc("GET /healthz", s.handleHealth)

	return withRequestLog(s.logger, mux)
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok\n"))
}

func (s *server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	bill := r.PathValue("bill")
	if err := validateBill(bill); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Calendar clients cannot set request headers on a subscription, so the
	// token has to travel in the query string.
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token query parameter", http.StatusBadRequest)
		return
	}

	now := time.Now().In(s.loc)
	outages, err := s.outages.Fetch(r.Context(), token, bill, now, now.Add(s.window))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	feed, err := buildICS(bill, outages, s.loc)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(feed)))
	w.Header().Set("Cache-Control", "no-store")
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
	case errors.Is(err, ErrUnauthorized):
		status, msg = http.StatusUnauthorized, "token rejected by the upstream API; it may have expired"
	case errors.Is(err, ErrUpstream), errors.Is(err, context.DeadlineExceeded):
		status, msg = http.StatusBadGateway, "upstream API is unavailable"
	}

	s.logger.Error("request failed", "route", r.Pattern, "status", status, "err", err)
	http.Error(w, msg, status)
}

func validateBill(bill string) error {
	if bill == "" {
		return errors.New("missing bill number")
	}
	if len(bill) > maxBillLen {
		return errors.New("bill number is too long")
	}
	for _, c := range bill {
		if c < '0' || c > '9' {
			return errors.New("bill number must contain digits only")
		}
	}
	return nil
}

// withRequestLog records the matched route rather than the raw URL, keeping
// the bill number and the token out of the logs.
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
