# Deploying barghvim

The upstream outage API is only reachable from inside Iran, so this has to run
on a host with Iranian network access. It currently runs on the `m10r` VM.

Upstream caps lookups at **20 calls an hour per source IP**, counted across the
whole server rather than per subscriber. Everything below assumes one instance
behind one IP; running a second instance halves the allowance each one gets.

## What lives where

| Path | Source |
|---|---|
| `/opt/barghvim/docker-compose.yml` | `deploy/docker-compose.yml` |
| `/etc/nginx/sites-available/barghvim` | `deploy/nginx/barghvim` |

## Releasing

Images are built by `.github/workflows/build.yml` on any `v*` tag — never on
the server. The workflow lints, tests, builds the image, boots it and probes
`/healthz`, and only then pushes to GHCR and cuts a GitHub release.

```bash
git tag v1.0.0
git push origin v1.0.0
```

Then on the VM:

```bash
cd /opt/barghvim
docker compose pull
docker compose up -d
```

Pinning a specific release instead of `latest`:

```bash
IMAGE_TAG=1.0.0 docker compose up -d
```

## Landing page

`barghvim.ir/` serves a landing page; every other path is the calendar feed.
The page itself is hosted on GitHub Pages and nginx relays the bare `/` to it,
because DNS cannot point one hostname at both Pages and this VM.

To enable it, in the repo's **Settings → Pages** set the source to
`main` / `/docs`. Leave the custom domain field **empty** — setting it makes
Pages redirect to `barghvim.ir` and the proxy loops.

The page is `docs/index.html`, a single self-contained file. Keep it that way:
a separate stylesheet would be fetched from `barghvim.ir/…`, which falls through
to the app's `/{bill}` route and 400s.

## TLS

Certificates are expected at `/etc/nginx/ssl/barghvim.ir/` as `fullchain.cer`
and `private.key`, matching the other sites on the box. `nginx -t` fails while
they are missing, so keep the `sites-enabled` symlink off until the cert is in
place:

```bash
sudo ln -s /etc/nginx/sites-available/barghvim /etc/nginx/sites-enabled/barghvim
sudo nginx -t && sudo systemctl reload nginx
```

## Configuration

The container needs no secrets — a bill number arrives per request. Optional
overrides go in `/opt/barghvim/.env`:

```sh
HOST_PORT=8081           # 8080 is taken by algofood
LOG_LEVEL=info
CACHE_TTL=6h              # how long a fetched report is reused
UPSTREAM_CALLS_PER_HOUR=18  # upstream allows 20/h per IP; leave headroom
IMAGE_TAG=latest
```
