# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

An HTTP service that fetches planned power outages from the public Tehran
outage-map API and serves them as a subscribable iCalendar feed. One route does
all the work:

```
GET /{bill}          # the .ics suffix is optional and stripped
```

A bill number is the only input. There is no token and no account — an earlier
version used the authenticated برق من API (`uiapi2.saapa.ir`) and a per-user
bearer token; that whole path is gone and must not come back.

## Commands

```bash
make check          # fmt + lint + test -race — run before pushing
make test           # go test -race ./...
make lint           # go vet + modernize
make modernize-fix  # apply modernize's suggestions
make run            # run on :8080
make image          # build the container
make help           # all targets
```

Single test or subtest (subtest names replace spaces with underscores):

```bash
go test -run TestFetchParsesOutages .
go test -run 'TestParseJalaliDateTime/leap_day' -v .
```

Tools (currently just `modernize`) are pinned in `go.mod` under the `tool` directive and run via `go tool`. Never add a `tools.go` or expect a binary on `PATH`; add one with `go get -tool <pkg>@<ver>`.

## Layout

Single flat `package main` at the repo root — deliberate, not accidental. This is a ~600-line binary nobody imports, so `internal/` and `pkg/` would be pure path depth. Do not reintroduce layered packages (`service/`, `repository/`, `handler/`).

| File | Owns |
|---|---|
| `main.go` | config from env, logger, wiring, graceful shutdown |
| `server.go` | routing, handlers, request logging, bill validation |
| `outages.go` | upstream `Client`, response decoding, error sentinels |
| `challenge.go` | ArvanCloud bot-check solver |
| `cache.go` | per-bill report cache, upstream call budget |
| `jalali.go` | Jalali ↔ Gregorian conversion |
| `calendar.go` | ICS rendering, event UIDs |

Routing is stdlib `net/http` (`GET /{bill}`). No web framework — don't add one.
A pattern like `GET /{bill}.ics` is illegal: a wildcard must span a whole path
segment. The suffix is trimmed in the handler instead.

## Upstream API quirks

These are the things that will bite you, and none are visible from a single file:

- **Every request hits an ArvanCloud bot check first.** An unrecognized client
  gets an HTML interstitial with HTTP 200, not an error — the status line says
  nothing, so `isChallenge` keys off the content type and the page's own cookie
  assignments. The page carries two cookie values as JSFuck expressions, which
  `challenge.go` evaluates in a goja interpreter with no host bindings. Its
  decoder `E` XORs by 6 and is applied twice, so each value is exactly what its
  expression evaluates to; do not add a decoding step. `testdata/challenge.html`
  is the real page, kept so a generator change fails a test rather than quietly
  emptying every subscriber's calendar.
- **The cookies live in the client's jar and must stay there.** Solving the
  challenge costs one of the twenty hourly calls. Re-solving per fetch doubles
  the cost. `NewClient` attaches a jar if `WithHTTPClient` supplies one without.
- **Upstream allows 20 calls an hour, per source IP.** Confirmed: after curl
  exhausted the quota, a browser with entirely different cookies on the same IP
  got the same 429, so the counter is not per session. The whole service shares
  one allowance, which is why `cache.go` exists at all. Anything that adds an
  upstream call per request will work in tests and fail in production.
- **HTTP 200 does not mean a bill was found.** An unknown bill number comes back
  as `[{"billId":null,...,"outages":[]}]`. A malformed one returns a bodyless
  400. Neither is an upstream failure.
- **Both timestamps carry their own date.** `outageDateTime` and
  `persianApproximateConnecTime` are full Jalali stamps, so an outage crossing
  midnight needs no repair. The previous API dated only the start and needed a
  rollover hack; do not reintroduce it. A reconnect that is not strictly after
  the start is a bad row and gets dropped.
- **Rows are skipped, not fatal.** One malformed row must never cost a
  subscriber their whole calendar. `Client.report` logs and drops bad rows.
- Clock times may be `24:00`, which `ptime.Date` normalizes into 00:00 the next
  day. `parseJalaliDateTime` allows hour 24 for this reason.

## Invariants worth preserving

- **`buildICS` output must stay deterministic.** Subscribers poll constantly; identical input has to render byte-identical or every poll looks like a change. This is why `DTSTAMP` derives from the event's start rather than `time.Now()`. `TestBuildICSIsDeterministic` guards it.
- **Never set `CLASS` on an event.** Google Calendar fetches a feed whose events carry `CLASS:PRIVATE`, answers 200, and then renders none of them — no error, no empty-calendar warning, nothing. Apple Calendar honours the property and displays the events, so the feed looks healthy in the one client you are most likely to test with. `TestBuildICSOmitsClass` pins it.
- **Event UIDs are stable identifiers.** Changing the hash or the `@barghvim` domain in `eventUID` makes every existing subscriber's calendar duplicate its events once.
- **The feed carries only outages that have not finished.** `upcoming` filters
  per request, not at fetch time, because a report is cached for hours and what
  counts as over moves while it sits there. An outage in progress is kept: its
  end is still ahead, and the reconnection estimate is the most useful thing in
  the calendar at that moment. The filter returns a new slice — the input is a
  cached report shared by every request for that bill, so filtering in place
  would strip outages from the cache for good. `TestUpcomingLeavesCacheAlone`
  pins that.
- **Serving a stale report beats serving none.** When a refresh fails and a
  cached copy is younger than `maxStale`, `reports.get` returns the stale copy
  with a nil error. An outage schedule a few days old is still useful; an empty
  calendar is not.
- **The feed must stay cacheable by clients.** `Cache-Control: public, max-age`
  plus a strong `ETag` lets a polling client answer itself with a 304. Reverting
  to `no-store` would put every poll back on the 20-an-hour budget.
- **Never log the raw URL.** The bill number is in the path, so the request
  logger records `r.Pattern` (the matched route) instead. Keep bill numbers out
  of logs.
- **Never return upstream text to callers.** `server.fail` maps `ErrBadBill` →
  404, `ErrThrottled` → 503 and `ErrUpstream` → 502, logs the full error, and
  writes a generic message. `TestHandleCalendarUpstreamFailures` asserts nothing
  leaks.
- **`_ "time/tzdata"` in `main.go` is load-bearing.** The runtime image is `scratch` with no `/usr/share/zoneinfo`; dropping the import makes `Asia/Tehran` fail to resolve at startup.
- Timezone is `Asia/Tehran` everywhere and is resolved once at boot, not per request. Iran has had no DST since 2022, so the offset is a constant +03:30.

## Testing

Tests are table-driven with a fake upstream (`httptest`), no mocking framework. Shared helpers live in the test files themselves: `tehran(t)` for the location, `upstream(t, handler)` for a `Client` wired to a fake API, `respondWith(t, bills...)` for canned lookups, `oneOutage()` for a well-formed bill entry, `challengePage(t)` for the captured interstitial, `discardLogger()` to silence skipped-row warnings, `newClock()` for time-dependent cache and budget tests, and `expire(t, srv)` to age the cache.

## Go version

Requires Go 1.26 (the `tool` directive pulls gopls, which forces it). `go.mod` and the `Dockerfile` base image must move together.

## Deployment

The upstream API is only reachable from inside Iran, so this cannot run on Vercel, Fly, or any foreign PaaS — it needs a host with Iranian network access. The service was previously deployed to Vercel; that path is gone and should not be restored. Configuration is environment variables only, documented in `.env.example`.

Because the 20-an-hour quota is counted per source IP, running two instances behind one IP halves what each one gets. Scale the cache TTL, not the instance count.

Images are built by CI on `v*` tags and pushed to GHCR; nothing is ever built on the server. The VM only holds `deploy/docker-compose.yml` and an nginx vhost — see `deploy/README.md`. The root `compose.yaml` is for local use and builds from source; `deploy/docker-compose.yml` is the one that runs in production and pulls a published image. Don't merge them.

The nginx vhost sets `access_log off` deliberately: the bill number rides in the path, so the default `combined` format would write it to disk on every calendar poll, undoing at the edge what the app is careful about internally.
