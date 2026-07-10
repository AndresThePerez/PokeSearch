# Pokesearch

Pokesearch is a fast, local search engine over 20,324 English Pokémon TCG cards. It combines a Go API, Elasticsearch relevance and facets, and an embedded vanilla-JavaScript gallery in one containerized application. Its signature feature is the query inspector: every UI search exposes the exact Elasticsearch DSL and writes the same query as structured JSON to the application log.

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
| `GET /api/search` | Fuzzy multi-field card search, filters, facets, sorting, and pagination | `q`, `id`, `supertype`, `types`, `rarity`, `series`, `hp_min`, `hp_max`, `sort`, `order`, `page`, `debug=1` |
| `GET /api/suggest` | Deduplicated card-name completion with a fuzzy retry | `q` |

Search responses contain 24 results per page plus live `supertype`, `types`, `rarity`, and `set_series` facets. Add `debug=1` to receive the generated DSL in the response. Every search and suggestion reaching Elasticsearch is also logged as one replayable JSON line.

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

## Milestone 2: deployment

Milestone 1 is intentionally local. Deployment, DNS/tunneling, Elasticsearch-volume backup and restore, and any remote Git workflow will be planned separately after local acceptance.
