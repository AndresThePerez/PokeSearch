# Pokesearch

Pokesearch is a fast, local search engine over 20,324 English Pokémon TCG cards. It combines a Go API, Elasticsearch relevance and facets, and an embedded vanilla-JavaScript gallery in one containerized application. Its signature feature is the right-side observability rail: every UI search exposes the exact Elasticsearch DSL, Elasticsearch latency, browser round-trip time, result and filter stats, and visible SLA targets while writing the same query as structured JSON to the application log.

## Quick start

```bash
docker compose up -d --build
docker compose --profile seed run --rm seed
curl -s http://localhost:8080/healthz
```

Open <http://localhost:8080>. The first seed streams the source tarball in memory and indexes 20,324 cards. Elasticsearch stays private to the Compose network; only the application is published on host port 8080.

## Development

The Go server defaults to `PORT=8080` and `ES_URL=http://127.0.0.1:9200`. Opt into a host-published development Elasticsearch instance with:

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d es
go run ./cmd/seed
PORT=8080 go run ./cmd/server
```

The seed command accepts:

- `-es URL` — Elasticsearch URL.
- `-ref REF` — `AndresThePerez/pokemon-tcg-data` Git ref; defaults to `master`. Pin a commit SHA when a reproducible corpus snapshot matters.
- `-force` — delete and recreate a populated `cards` index.

Source JSON is ingestion-only: the tarball is streamed, transformed in memory, and never written into this repository or the container filesystem.

Run the code checks with:

```bash
go test ./...
go vet ./...
```

## API

| Endpoint | Purpose | Parameters |
|---|---|---|
| `GET /healthz` | Elasticsearch reachability and indexed document count | — |
| `GET /api/search` | Fuzzy multi-field card search, filters, facets, sorting, and pagination | `q`, `id`, `supertype`, `types`, `set`, `rarity`, `series`, `hp_min`, `hp_max`, `sort`, `order`, `page`, `debug=1` |
| `GET /api/suggest` | Deduplicated card-name completion with a fuzzy retry | `q` |

Search responses contain 24 results per page, Elasticsearch's `took_ms`, and live `supertype`, `types`, `rarity`, `set_series`, and readable `sets` facets. The `set` parameter takes an exact set ID; combine it with `q` to search within that set. Add `debug=1` to receive the generated DSL in the response. Every search and suggestion reaching Elasticsearch is also logged as one replayable JSON line.

The API and index retain the source dataset's canonical TCG type values. The interface presents `Metal` as **Steel** and `Colorless` as **Normal**, including filter labels, active-filter chips, attack costs, and card details.

## Architecture

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

The production Compose topology publishes only the Go application. `docker-compose.dev.yml` is deliberately opt-in so a plain `docker compose up` never exposes Elasticsearch on the host.

## Deployment (home server)

The production instance runs on the home server (`altof@server`) in `~/apps/pokesearch`, published on host port **8083** (8080–8082 are taken by other services). The published port comes from a git-ignored `.env` file next to the compose files:

```bash
printf 'APP_PORT=8083\n' > .env
docker compose -f docker-compose.yml -f docker-compose.server.yml up -d --build
```

`docker-compose.server.yml` adds `restart: unless-stopped` and memory caps (`es` 1g — twice the 512m JVM heap, per Elastic's container guidance; `app` 256m) so the stack coexists with the host's other tenants. Elasticsearch remains on the Compose-internal network with no host port in any topology.

### Seeding

Seed once per environment, pinned for reproducibility:

```bash
docker compose -f docker-compose.yml -f docker-compose.server.yml --profile seed run --rm seed \
  -es http://es:9200 -ref 0af6250a22495e4a3e9f60ff45fc3fedc2e0563d
```

`0af6250a22495e4a3e9f60ff45fc3fedc2e0563d` is the `pokemon-tcg-data` `master` commit as of 2026-07-10 and yields exactly **20,324** documents (`/healthz` → `{"docs":20324,"status":"ok"}`). A populated index makes reseeding a no-op unless `-force` is passed.

### Updating the server

The server has no git remote. Changes are committed on the workstation, then shipped as a bundle:

```bash
# workstation
git bundle create ~/pokesearch.bundle --all && scp ~/pokesearch.bundle server:~/apps/
# server
cd ~/apps/pokesearch && git pull ../pokesearch.bundle master
docker compose -f docker-compose.yml -f docker-compose.server.yml up -d --build
```

Never edit the server clone in place.

### Backup and restore

The index is write-once: one verified backup after seeding is sufficient (no cron). All commands run on the server in `~/apps/pokesearch`; each was exercised against the live volume on 2026-07-10.

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
4. **Full rebuild** — re-clone from the bundle, `up -d --build`, reseed.

Rollback for public exposure: restore `/etc/cloudflared/config.yml.bak-<DATE>` over the live config, `sudo systemctl restart cloudflared`, then re-verify `daybook.andrestheperez.com`, the apex, and `www` all return 200.

### Public exposure

`https://pokesearch.andrestheperez.com` is served by the host's existing Cloudflare Tunnel: one ingress rule (`hostname: pokesearch.andrestheperez.com` → `http://localhost:8083`) above the 404 catch-all in `/etc/cloudflared/config.yml`. The wildcard DNS record already routes the subdomain to the tunnel; no DNS changes are involved. The same tunnel serves other production sites — after any config change, regression-check them all.
