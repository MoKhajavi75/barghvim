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
		outages: NewClient(loc, WithEndpoint(cfg.Endpoint), WithLogger(logger)),
		loc:     loc,
		window:  cfg.Window,
		logger:  logger,
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
		logger.Info("listening", "version", version, "addr", cfg.Addr, "window", cfg.Window.String())
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
	Addr     string
	Endpoint string
	Window   time.Duration
	LogLevel slog.Level
}

func loadConfig() (config, error) {
	cfg := config{
		Addr:     cmp.Or(os.Getenv("ADDR"), portAddr(os.Getenv("PORT")), ":8080"),
		Endpoint: cmp.Or(os.Getenv("UPSTREAM_URL"), defaultEndpoint),
		Window:   defaultWindow,
		LogLevel: slog.LevelInfo,
	}

	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return config{}, fmt.Errorf("parsing LOG_LEVEL %q: %w", v, err)
		}
	}

	if v := os.Getenv("OUTAGE_WINDOW_DAYS"); v != "" {
		days, err := strconv.Atoi(v)
		if err != nil || days < 1 || days > maxWindowDays {
			return config{}, fmt.Errorf("OUTAGE_WINDOW_DAYS must be an integer in 1..%d, got %q", maxWindowDays, v)
		}
		cfg.Window = time.Duration(days) * 24 * time.Hour
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
