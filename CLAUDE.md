# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A stateless HTTP service that fetches planned power outages from the Iranian برق من API and serves them as a subscribable iCalendar feed. One route does all the work:

```
GET /v1/{bill}/cal.ics?token=<برق من token>
```

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
| `jalali.go` | Jalali ↔ Gregorian conversion |
| `calendar.go` | ICS rendering, event UIDs |

Routing is stdlib `net/http` (`GET /v1/{bill}/cal.ics`). No web framework — don't add one.

## Upstream API quirks

These are the things that will bite you, and none are visible from a single file:

- **HTTP 200 does not mean success.** The API signals application-level failures in the response body's `status` field. `Fetch` checks the HTTP status *and* the body status, and maps both to the same sentinels.
- **A report row carries one date and two clock times.** An outage crossing midnight therefore comes back with an end that sorts before its start. `reportItem.outage` rolls the end forward one day — but only when it is *strictly* before the start. An end equal to the start is a zero-length row and must be rejected, not stretched into 24 hours. This exact distinction was a real bug; `TestFetchSkipsUnusableRows` pins it.
- **Rows are skipped, not fatal.** One malformed row must never cost a subscriber their whole calendar. `Client.collect` logs and drops bad rows.
- Clock times may be `24:00`, which `ptime.Date` normalizes into 00:00 the next day. `parseJalaliDateTime` allows hour 24 for this reason.

## Invariants worth preserving

- **`buildICS` output must stay deterministic.** Subscribers poll constantly; identical input has to render byte-identical or every poll looks like a change. This is why `DTSTAMP` derives from the event's start rather than `time.Now()`. `TestBuildICSIsDeterministic` guards it.
- **Event UIDs are stable identifiers.** Changing the hash or the `@barghvim` domain in `eventUID` makes every existing subscriber's calendar duplicate its events once.
- **Never log the raw URL.** The token has to travel in the query string because calendar clients cannot set headers on a subscription, so the request logger records `r.Pattern` (the matched route) instead of the path or query. Keep the bill number and token out of logs.
- **Never return upstream text to callers.** `server.fail` maps `ErrUnauthorized` → 401 and `ErrUpstream` → 502, logs the full error, and writes a generic message. `TestHandleCalendarUpstreamFailures` asserts nothing leaks.
- **`_ "time/tzdata"` in `main.go` is load-bearing.** The runtime image is `scratch` with no `/usr/share/zoneinfo`; dropping the import makes `Asia/Tehran` fail to resolve at startup.
- Timezone is `Asia/Tehran` everywhere and is resolved once at boot, not per request. Iran has had no DST since 2022, so the offset is a constant +03:30.

## Testing

Tests are table-driven with a fake upstream (`httptest`), no mocking framework. Shared helpers live in the test files themselves: `tehran(t)` for the location, `upstream(t, handler)` for a `Client` wired to a fake API, `respondWith(t, body)` for canned report responses, `discardLogger()` to silence skipped-row warnings.

## Go version

Requires Go 1.26 (the `tool` directive pulls gopls, which forces it). `go.mod` and the `Dockerfile` base image must move together.

## Deployment

The upstream API is only reachable from inside Iran, so this cannot run on Vercel, Fly, or any foreign PaaS — it needs a host with Iranian network access. The service was previously deployed to Vercel; that path is gone and should not be restored. Configuration is environment variables only, documented in `.env.example`.

Images are built by CI on `v*` tags and pushed to GHCR; nothing is ever built on the server. The VM only holds `deploy/docker-compose.yml` and an nginx vhost — see `deploy/README.md`. The root `compose.yaml` is for local use and builds from source; `deploy/docker-compose.yml` is the one that runs in production and pulls a published image. Don't merge them.

The nginx vhost sets `access_log off` deliberately: the token rides in the query string and the bill number in the path, so the default `combined` format would write both to disk on every calendar poll, undoing at the edge what the app is careful about internally.
