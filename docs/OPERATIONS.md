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
