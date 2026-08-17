package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"time"
)

const (
	// defaultEndpoint is the برق من planned-blackouts report.
	defaultEndpoint = "https://uiapi2.saapa.ir/api/ebills/PlannedBlackoutsReport"

	// defaultWindow is how far ahead outages are requested.
	defaultWindow = 7 * 24 * time.Hour

	// maxWindowDays bounds the configurable lookahead.
	maxWindowDays = 60

	// maxResponseBytes caps how much of an upstream reply is decoded.
	maxResponseBytes = 4 << 20
)

// Errors returned by Client.Fetch, wrapped with detail for logs. Handlers
// match on these to pick a status code without leaking upstream internals.
var (
	// ErrUnauthorized means the caller's token was rejected upstream.
	ErrUnauthorized = errors.New("upstream rejected the token")

	// ErrUpstream means the upstream API was unreachable or answered unusably.
	ErrUpstream = errors.New("upstream request failed")
)

// Outage is a single planned power outage window, in the Tehran timezone.
type Outage struct {
	Start time.Time
	End   time.Time
}

// Client fetches planned outages from the برق من API.
type Client struct {
	http     *http.Client
	endpoint string
	loc      *time.Location
	logger   *slog.Logger
}

// Option customizes a Client built by NewClient.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.http = c }
}

// WithEndpoint overrides the upstream URL, mainly for tests.
func WithEndpoint(url string) Option {
	return func(cl *Client) { cl.endpoint = url }
}

// WithLogger sets the logger used to report skipped outage rows.
func WithLogger(l *slog.Logger) Option {
	return func(cl *Client) { cl.logger = l }
}

// NewClient returns a Client that reports outage times in loc.
func NewClient(loc *time.Location, opts ...Option) *Client {
	c := &Client{
		http:     defaultHTTPClient(),
		endpoint: defaultEndpoint,
		loc:      loc,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// Fetch returns the outages planned for bill between from and to, sorted by
// start time. Rows the API returns in an unusable shape are skipped and
// logged rather than failing the whole feed.
func (c *Client) Fetch(ctx context.Context, token, bill string, from, to time.Time) ([]Outage, error) {
	payload, err := json.Marshal(reportRequest{
		BillID: bill,
		From:   jalaliDate(from, c.loc),
		To:     jalaliDate(to, c.loc),
	})
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%w: http %d", ErrUnauthorized, resp.StatusCode)
	default:
		return nil, fmt.Errorf("%w: http %d", ErrUpstream, resp.StatusCode)
	}

	var body reportResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: decoding response: %w", ErrUpstream, err)
	}

	// The API answers 200 even for application-level failures, signalling the
	// real outcome in the body.
	if body.Status == http.StatusUnauthorized || body.Status == http.StatusForbidden {
		return nil, fmt.Errorf("%w: body status %d: %s", ErrUnauthorized, body.Status, body.Message)
	}
	if body.Status != http.StatusOK {
		return nil, fmt.Errorf("%w: body status %d: %s", ErrUpstream, body.Status, body.Message)
	}

	return c.collect(body.Data), nil
}

func (c *Client) collect(items []reportItem) []Outage {
	out := make([]Outage, 0, len(items))
	for _, item := range items {
		o, err := item.outage(c.loc)
		if err != nil {
			c.logger.Warn("skipping unparsable outage row", "err", err)
			continue
		}
		out = append(out, o)
	}

	slices.SortFunc(out, func(a, b Outage) int { return a.Start.Compare(b.Start) })
	return out
}

type reportRequest struct {
	BillID string `json:"bill_id"`
	From   string `json:"from_date"` // Jalali yyyy/mm/dd
	To     string `json:"to_date"`   // Jalali yyyy/mm/dd
}

type reportResponse struct {
	Status  int          `json:"status"`
	Message string       `json:"message"`
	Data    []reportItem `json:"data"`
}

type reportItem struct {
	Date  string `json:"outage_date"`       // Jalali yyyy/mm/dd
	Start string `json:"outage_start_time"` // HH:MM
	Stop  string `json:"outage_stop_time"`  // HH:MM
}

func (r reportItem) outage(loc *time.Location) (Outage, error) {
	start, err := parseJalaliDateTime(r.Date, r.Start, loc)
	if err != nil {
		return Outage{}, fmt.Errorf("start: %w", err)
	}

	end, err := parseJalaliDateTime(r.Date, r.Stop, loc)
	if err != nil {
		return Outage{}, fmt.Errorf("end: %w", err)
	}

	// A row carries one date and two clock times, so an outage running past
	// midnight comes back with an end that sorts before its start. Only a
	// strictly earlier end means a rollover; an equal one is a zero-length
	// row, which is rejected below rather than stretched into a full day.
	if end.Before(start) {
		end = end.AddDate(0, 0, 1)
	}
	if !end.After(start) {
		return Outage{}, fmt.Errorf("end %q is not after start %q on %s", r.Stop, r.Start, r.Date)
	}

	return Outage{Start: start, End: end}, nil
}
