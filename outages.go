package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	// defaultEndpoint is the public planned-outage lookup behind the Tehran
	// distribution company's outage map. It needs a bill number and nothing
	// else — no account, no token.
	defaultEndpoint = "https://shahab.tbtb.ir/public/map/v2/GetOutagesListByBillId"

	// maxResponseBytes caps how much of an upstream reply is read.
	maxResponseBytes = 4 << 20
)

// Errors returned by Client.Fetch, wrapped with detail for logs. Handlers
// match on these to pick a status code without leaking upstream internals.
var (
	// ErrUpstream means the upstream API was unreachable or answered unusably.
	ErrUpstream = errors.New("upstream request failed")

	// ErrThrottled means the upstream call budget is spent. Upstream allows
	// 20 calls an hour per source IP and answers 429 past that.
	ErrThrottled = errors.New("upstream call budget exhausted")

	// ErrBadBill means upstream rejected the bill number outright.
	ErrBadBill = errors.New("upstream rejected the bill number")
)

// Outage is a single planned power outage window, in the Tehran timezone.
type Outage struct {
	Start  time.Time
	End    time.Time
	Reason string
}

// Report is everything upstream knows about one bill number.
type Report struct {
	Bill      string
	Address   string
	Latitude  float64
	Longitude float64
	HasCoords bool
	Outages   []Outage
}

// Client fetches planned outages from the outage-map API.
type Client struct {
	http     *http.Client
	endpoint string
	loc      *time.Location
	budget   *budget
	logger   *slog.Logger
}

// Option customizes a Client built by NewClient.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client. NewClient attaches a
// cookie jar if the replacement has none.
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

// WithBudget sets the upstream call allowance.
func WithBudget(b *budget) Option {
	return func(cl *Client) { cl.budget = b }
}

// NewClient returns a Client that reports outage times in loc.
func NewClient(loc *time.Location, opts ...Option) *Client {
	c := &Client{
		http:     defaultHTTPClient(),
		endpoint: defaultEndpoint,
		loc:      loc,
		budget:   newBudget(defaultCallsPerHour, time.Hour),
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(c)
	}
	// The challenge cookies have nowhere to live without a jar, which would
	// mean re-solving the bot check on every call.
	if c.http.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			panic(fmt.Sprintf("building cookie jar: %v", err)) // cookiejar.New(nil) cannot fail
		}
		c.http.Jar = jar
	}
	return c
}

func defaultHTTPClient() *http.Client {
	// The jar carries the bot-check cookies from one request to the next, so
	// the challenge is solved once rather than on every fetch.
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(fmt.Sprintf("building cookie jar: %v", err)) // cookiejar.New(nil) cannot fail
	}

	return &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
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

// Fetch returns the outages planned for bill, sorted by start time. Rows the
// API returns in an unusable shape are skipped and logged rather than failing
// the whole feed.
func (c *Client) Fetch(ctx context.Context, bill string) (Report, error) {
	body, contentType, err := c.post(ctx, bill)
	if err != nil {
		return Report{}, err
	}

	// A client the bot check does not recognize gets an HTML interstitial in
	// place of the API response. Solve it, keep the cookies, and ask again.
	if isChallenge(contentType, body) {
		cookies, err := solveChallenge(body)
		if err != nil {
			return Report{}, fmt.Errorf("%w: solving bot challenge: %w", ErrUpstream, err)
		}
		c.storeCookies(cookies)
		c.logger.Info("solved upstream bot challenge", "cookies", len(cookies))

		if body, contentType, err = c.post(ctx, bill); err != nil {
			return Report{}, err
		}
		if isChallenge(contentType, body) {
			return Report{}, fmt.Errorf("%w: bot challenge still unsolved after retry", ErrUpstream)
		}
	}

	if !isJSON(contentType) {
		return Report{}, fmt.Errorf("%w: unexpected content type %q", ErrUpstream, contentType)
	}

	var bills []billResponse
	if err := json.Unmarshal(body, &bills); err != nil {
		return Report{}, fmt.Errorf("%w: decoding response: %w", ErrUpstream, err)
	}
	if len(bills) == 0 {
		return Report{Bill: bill}, nil
	}

	return c.report(bill, bills[0]), nil
}

// post sends one lookup and returns the body and content type. Every call
// spends one unit of the upstream budget, the challenge round included.
func (c *Client) post(ctx context.Context, bill string) ([]byte, string, error) {
	if !c.budget.allow() {
		return nil, "", fmt.Errorf("%w: no calls left this hour", ErrThrottled)
	}

	form := url.Values{"BillId": {bill}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, "", fmt.Errorf("%w: reading response: %w", ErrUpstream, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return body, resp.Header.Get("Content-Type"), nil
	case http.StatusTooManyRequests:
		// Upstream's counter is the real one; ours only estimates it. Believe
		// upstream and stop calling until the local budget refills.
		c.budget.drain()
		return nil, "", fmt.Errorf("%w: upstream answered 429", ErrThrottled)
	case http.StatusBadRequest:
		return nil, "", fmt.Errorf("%w: upstream answered 400", ErrBadBill)
	default:
		return nil, "", fmt.Errorf("%w: http %d", ErrUpstream, resp.StatusCode)
	}
}

func (c *Client) storeCookies(cookies []*http.Cookie) {
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return
	}
	c.http.Jar.SetCookies(u, cookies)
}

// report turns one decoded bill entry into a Report, dropping rows that do
// not parse. One malformed row must never cost a subscriber their calendar.
func (c *Client) report(bill string, b billResponse) Report {
	rep := Report{
		Bill:    bill,
		Address: strings.TrimSpace(deref(b.Address)),
		Outages: make([]Outage, 0, len(b.Outages)),
	}
	if b.Latitude != nil && b.Longitude != nil {
		rep.Latitude, rep.Longitude, rep.HasCoords = *b.Latitude, *b.Longitude, true
	}

	for _, item := range b.Outages {
		o, err := item.outage(c.loc)
		if err != nil {
			c.logger.Warn("skipping unusable outage row", "err", err)
			continue
		}
		rep.Outages = append(rep.Outages, o)
	}

	slices.SortFunc(rep.Outages, func(a, b Outage) int { return a.Start.Compare(b.Start) })
	return rep
}

// billResponse is one entry of the API's top-level array. Every field is
// nullable: an unknown bill number comes back as a fully null entry with an
// empty outage list rather than as an error.
type billResponse struct {
	BillID    *string      `json:"billId"`
	Latitude  *float64     `json:"latitude"`
	Longitude *float64     `json:"longitude"`
	Address   *string      `json:"address"`
	Outages   []outageItem `json:"outages"`
}

type outageItem struct {
	Number int    `json:"outageNumber"`
	Reason string `json:"consumerOutageReason"`
	// Start and Reconnect are Jalali "yyyy/mm/dd HH:MM" stamps. Both carry
	// their own date, so an outage running past midnight needs no repair.
	Start     string `json:"outageDateTime"`
	Reconnect string `json:"persianApproximateConnecTime"`
}

func (o outageItem) outage(loc *time.Location) (Outage, error) {
	start, err := parseJalaliStamp(o.Start, loc)
	if err != nil {
		return Outage{}, fmt.Errorf("outage %d start: %w", o.Number, err)
	}

	end, err := parseJalaliStamp(o.Reconnect, loc)
	if err != nil {
		return Outage{}, fmt.Errorf("outage %d reconnect: %w", o.Number, err)
	}

	if !end.After(start) {
		return Outage{}, fmt.Errorf("outage %d reconnect %q is not after start %q", o.Number, o.Reconnect, o.Start)
	}

	return Outage{Start: start, End: end, Reason: strings.TrimSpace(o.Reason)}, nil
}

// isJSON reports whether a Content-Type header names a JSON body.
func isJSON(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "json")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
