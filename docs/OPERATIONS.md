# Operations

This file is the topology, deployment, seeding, backup and recovery runbook for a PokéSearch stack on any Docker host.

## Topology

```text
browser :8080
     │
     ▼
Go server ── embedded HTML/CSS/JS
     │
     │ Compose-internal HTTP :9200
     ▼
Elasticsearch 8.19 ── es-data volume
     ▲
     │ one-shot /seed profile
pokemon-tcg-data tarball (streamed from GitHub)
```

## Deploying to a host

The stack is deployed by pulling the repository onto a host and building there. Substitute your own values:

| Variable | Example | Meaning |
|---|---|---|
| `DEPLOY_HOST` | `user@server` | SSH target running Docker |
| `DEPLOY_DIR` | `/srv/pokesearch` | Checkout location on that host |
| `APP_PORT` | `8090` | Host port to publish the application on (defaults to `8080`) |
| `SEED_REF` | `39a26a14…` | `pokemon-tcg-data` commit to index |

```bash
ssh "$DEPLOY_HOST"
cd "$DEPLOY_DIR"
git pull
printf 'APP_PORT=%s\n' "$APP_PORT" > .env      # git-ignored
docker compose -f docker-compose.yml -f docker-compose.server.yml up -d --build
```

`docker-compose.server.yml` adds `restart: unless-stopped` and memory caps (`es` 2g, four times the 512m JVM heap, so the page cache is not starved; `app` 256m) so the stack coexists with a host's other tenants. Elasticsearch remains on the Compose-internal network with no host port in any topology.

### Seeding

Seed once per environment, pinned for reproducibility:

```bash
docker compose -f docker-compose.yml -f docker-compose.server.yml --profile seed run --rm seed \
  -es http://es:9200 -ref "$SEED_REF"
```

`39a26a144c8b6ef6c2fb17b2c29d0bb7121e3a11` is the `pokemon-tcg-data` `master` commit as of 2026-09-16 and yields exactly **20,635** documents (`/healthz` → `{"docs":20635,"status":"ok"}`). It is the ref pinned in `docker-compose.yml`. A populated index makes reseeding a no-op unless `-force` is passed.

### After a reseed

Restart `app` after any reseed of an index that already holds cards. Four things are cached for the life of the process: the set catalog behind the `sets` facet of `/api/search`, the `/api/stats` payload, the seed provenance (`seed.ref` and `seed.seeded_at`) that `/api/meta` and the `/api/v1/` answers report, and the `/api/v1/sets` list. A reseed changes none of them in a running app, so until it restarts they describe the old corpus. Searches and card lookups are not cached and show the new index at once.

```bash
docker compose -f docker-compose.yml -f docker-compose.server.yml restart app
```

The first seed into an empty index needs no restart, because an empty answer, or a missing seed stamp, is never cached.

### Backup and restore

The index is write-once: one verified backup after seeding is sufficient (no cron). All commands run on the deployment host in `$DEPLOY_DIR`.

Backup (≈30s downtime; the tarball is ~7MB):

```bash
mkdir -p ~/backups
docker compose -f docker-compose.yml -f docker-compose.server.yml stop es
docker run --rm -v pokesearch_es-data:/data -v ~/backups:/out alpine \
  tar czf /out/pokesearch-es-data-$(date +%F).tgz -C /data .
docker compose -f docker-compose.yml -f docker-compose.server.yml start es
```

Restore (into the live volume — stop the stack first):

```bash
docker compose -f docker-compose.yml -f docker-compose.server.yml stop es app
docker run --rm -v pokesearch_es-data:/data -v ~/backups:/in alpine \
  sh -c 'rm -rf /data/* && tar xzf /in/pokesearch-es-data-<DATE>.tgz -C /data'
docker compose -f docker-compose.yml -f docker-compose.server.yml start es app
```

Recovery ladder, cheapest first:

1. **Container restart** — the `es-data` volume persists the index.
2. **Reseed** with the pinned ref (see Seeding above) — deterministic, takes seconds. Restart `app` afterwards (see After a reseed above).
3. **Restore** the backup tarball into `pokesearch_es-data` (commands above).
4. **Full rebuild** — re-clone, `up -d --build`, reseed.

## Alerting

A query Elasticsearch rejects is a client `400`, not a `503`, so it does not show in 5xx monitoring. Each such request writes its query line to the application log with `"status":400` and an `error` that begins with `elasticsearch: request rejected`, followed by the cluster's own reason:

```text
{"time":"…","request_id":"…","endpoint":"search", … ,"status":400,"error":"elasticsearch: request rejected: es 400 Bad Request: {…}"}
```

This applies to search, suggest, explain and compare. Alert on that prefix if a class of valid queries starts being rejected:

```bash
docker compose -f docker-compose.yml -f docker-compose.server.yml logs app | grep 'elasticsearch: request rejected'
```

Every other Elasticsearch failure is still a `503` with code `es_unavailable`, and the `/api/v1/` routes answer all of them that way.

## Requests in flight

The server handles at most `MAX_IN_FLIGHT` requests at once: 128 unless the app's `environment:` sets another value. A value that is not a whole number of at least 1 stops the server at start-up, and the start-up line `PokéSearch listening` names the value in use as `max_in_flight`. A request that arrives while every place is taken is answered at once with `503`, `Retry-After: 1` and `Connection: close`: under `/api/` in the error envelope with code `overloaded`, elsewhere as plain text. It is not queued, its parameters are not read, nothing is sent to Elasticsearch and it writes no query line, only its access line:

```text
{"time":"2026-11-20T14:23:16.001Z","level":"INFO","msg":"access","method":"GET","path":"/api/search","status":503,"bytes":122,"dur_ms":0,"request_id":"example-refused-1"}
```

`GET /livez` and `GET /healthz` are never refused, so the container healthcheck (`/server -ping` asks `/livez`) and an uptime monitor stay green while the server sheds load. A rise in `503` access lines while `/healthz` answers `200` is load, not an outage. An `es_unavailable` `503` also writes a query line, or a `healthz check failed` line, with the same `request_id`; a `503` access line with no other line for its `request_id` is a refusal.

The default was measured on a build held to 256 MB with no swap: 128 of the costliest accepted searches at once hold about 51 MiB, and bursts of up to 10,000 requests at once peaked at no more than 128 MiB, the process staying up and answering its health probes. `docker-compose.server.yml` keeps the app at `mem_limit: 256m` and sets `GOMEMLIMIT=180MiB`, a soft target that makes the Go collector work harder before the hard limit is reached; it is a second belt, not a guarantee. Raise `MAX_IN_FLIGHT` only together with the memory limit. The cap does not tell clients apart: rate-limiting one client is the job of the proxy in front.
