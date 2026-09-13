# Operations

This file is the seeding, backup and recovery runbook for a PokéSearch stack on any Docker host.

## Topology

```text
browser :8080
     │
     ▼
Go server ── embedded HTML/CSS/JS
     │
     │ Compose-internal HTTP :9200
     ▼
Elasticsearch 8.15 ── es-data volume
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
| `SEED_REF` | `0af6250a…` | `pokemon-tcg-data` commit to index |

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

`0af6250a22495e4a3e9f60ff45fc3fedc2e0563d` is the `pokemon-tcg-data` `master` commit as of 2026-07-10 and yields exactly **20,324** documents (`/healthz` → `{"docs":20324,"status":"ok"}`). It is the ref pinned in `docker-compose.yml`. A populated index makes reseeding a no-op unless `-force` is passed.

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
2. **Reseed** with the pinned ref (see Seeding above) — deterministic, takes seconds.
3. **Restore** the backup tarball into `pokesearch_es-data` (commands above).
4. **Full rebuild** — re-clone, `up -d --build`, reseed.
