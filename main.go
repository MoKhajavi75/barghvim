// Command barghvim serves planned power outages from the Iranian "برق من"
// (Bargh-e Man) API as a subscribable iCalendar feed.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	// Embed the IANA timezone database so Asia/Tehran resolves even in a
	// scratch container that carries no /usr/share/zoneinfo.
	_ "time/tzdata"
)

// timezone is the only zone the upstream API reports outages in.
const timezone = "Asia/Tehran"

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("barghvim exited", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return fmt.Errorf("loading timezone %q: %w", timezone, err)
	}

	srv := &server{
		outages: NewClient(loc,
			WithEndpoint(cfg.Endpoint),
			WithLogger(logger),
			WithBudget(newBudget(cfg.CallsPerHour, time.Hour)),
		),
		reports: newReports(cfg.CacheTTL),
		loc:     loc,
		ttl:     cfg.CacheTTL,
		logger:  logger,
		now:     time.Now,
	}

	httpSrv := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv.routes(),
		// net/http ships with no timeouts; without these a single slow client
		// can hold a connection open indefinitely.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "version", version, "addr", cfg.Addr, "cacheTTL", cfg.CacheTTL.String(), "callsPerHour", cfg.CallsPerHour)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serving on %s: %w", cfg.Addr, err)
	case <-ctx.Done():
		logger.Info("shutting down")
		// The signal context is already canceled, so the drain needs a fresh
		// deadline of its own.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

type config struct {
	Addr         string
	Endpoint     string
	CacheTTL     time.Duration
	CallsPerHour int
	LogLevel     slog.Level
}

func loadConfig() (config, error) {
	cfg := config{
		Addr:         cmp.Or(os.Getenv("ADDR"), portAddr(os.Getenv("PORT")), ":8080"),
		Endpoint:     cmp.Or(os.Getenv("UPSTREAM_URL"), defaultEndpoint),
		CacheTTL:     defaultTTL,
		CallsPerHour: defaultCallsPerHour,
		LogLevel:     slog.LevelInfo,
	}

	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return config{}, fmt.Errorf("parsing LOG_LEVEL %q: %w", v, err)
		}
	}

	if v := os.Getenv("CACHE_TTL"); v != "" {
		ttl, err := time.ParseDuration(v)
		if err != nil || ttl < time.Minute || ttl > maxCacheTTL {
			return config{}, fmt.Errorf("CACHE_TTL must be a duration in 1m..%s, got %q", maxCacheTTL, v)
		}
		cfg.CacheTTL = ttl
	}

	if v := os.Getenv("UPSTREAM_CALLS_PER_HOUR"); v != "" {
		calls, err := strconv.Atoi(v)
		if err != nil || calls < 1 || calls > maxCallsPerHour {
			return config{}, fmt.Errorf("UPSTREAM_CALLS_PER_HOUR must be an integer in 1..%d, got %q", maxCallsPerHour, v)
		}
		cfg.CallsPerHour = calls
	}

	return cfg, nil
}

// portAddr turns a bare PORT value into a listen address, or returns "" so
// callers can fall through to the next default.
func portAddr(port string) string {
	if port == "" {
		return ""
	}
	return ":" + port
}
