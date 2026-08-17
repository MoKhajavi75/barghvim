# Deploying barghvim

The upstream برق من API is only reachable from inside Iran, so this has to run
on a host with Iranian network access. It currently runs on the `m10r` VM.

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

The container needs no secrets — a subscriber's token and bill number arrive
per request. Optional overrides go in `/opt/barghvim/.env`:

```sh
HOST_PORT=8081           # 8080 is taken by algofood
LOG_LEVEL=info
OUTAGE_WINDOW_DAYS=7
IMAGE_TAG=latest
```
