# PokéSearch

[![ci](https://github.com/AndresThePerez/PokeSearch/actions/workflows/ci.yml/badge.svg)](https://github.com/AndresThePerez/PokeSearch/actions/workflows/ci.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

### **[Live demo → pokesearch.andrestheperez.com](https://pokesearch.andrestheperez.com)**

PokéSearch is a search engine over 20,635 English Pokémon TCG cards: a Go API on Elasticsearch with a dependency-free frontend, shipped as a single containerized binary. What makes it worth a look is that the ranking explains itself on the page: every card shows which of four named branches matched it, the Ranking comparison panel ranks the same query under two weight profiles, and “Why this card?” breaks one card's score into parts that add back up to it.

![PokéSearch search results with the observability rail open](docs/media/hero.png)

Try these on the [live site](https://pokesearch.andrestheperez.com). Each one exercises a different branch of the ranking:

- [`charizard`](https://pokesearch.andrestheperez.com/?q=charizard) — an exact name, so all four branches match.
- [`pika`](https://pokesearch.andrestheperez.com/?q=pika) — a name prefix; the `prefix` branch finds the Pikachu cards.
- [`pikuchu`](https://pokesearch.andrestheperez.com/?q=pikuchu) — a typo; the `fuzzy-name` branch still finds Pikachu.
- [`flip a coin`](https://pokesearch.andrestheperez.com/?q=flip%20a%20coin) — card text; the `text` branch finds cards by what they do.
- [`Mitsuhiro Arita`](https://pokesearch.andrestheperez.com/?q=Mitsuhiro%20Arita) — an artist; only the `text` branch matches.

Every result page carries an observability rail: the exact Elasticsearch DSL that answered the query, the cluster's own latency broken out from the browser round trip, a sparkline of the session's last twenty requests, and the live SLA targets — while the same query goes to the application log as one replayable JSON line tagged with the request ID printed on screen.

Relevance is equally legible. A text query fans into four *named*, boosted `should` branches; Elasticsearch echoes which branches each hit matched, so the grid renders them as per-card badges and the boost hierarchy becomes something you can watch shift down the page. `GET /api/explain` takes it one card deeper through Lucene's `_explain`, and because a `should` query's clauses sum, the modal's score bars add back up to the number the card was ranked by. The treatment generalizes: `#stats` profiles the entire archive in hand-rolled CSS charts and shows the aggregation DSL behind them in the same inspector.

## Quick start

```bash
docker compose up -d --build
docker compose --profile seed run --rm seed
curl -s http://localhost:8080/healthz
```

Open <http://localhost:8080>. The first seed streams the source tarball through memory and indexes 20,635 cards; it never writes them to disk. Elasticsearch stays private to the Compose network — only the application is published.

Publish on a different host port with `APP_PORT`:

```bash
APP_PORT=8081 docker compose up -d --build
```

![Rarity-driven holographic card modal](docs/media/holo.gif)

## Development

The Go server defaults to `PORT=8080` and `ES_URL=http://127.0.0.1:9200`, and handles at most `MAX_IN_FLIGHT=128` requests at once (see `overloaded` under *Errors*); a `MAX_IN_FLIGHT` that is not a whole number of at least 1 stops it from starting. Opt into a host-published development Elasticsearch with:

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d es
go run ./cmd/seed
PORT=8080 go run ./cmd/server
```

`docker-compose.dev.yml` binds Elasticsearch to `127.0.0.1:9200` only — never `0.0.0.0`.

The seed command accepts:

- `-es URL` — Elasticsearch URL. Defaults to `http://127.0.0.1:9200`.
- `-ref REF` — `AndresThePerez/pokemon-tcg-data` Git ref; defaults to `master`. Pin a commit SHA when a reproducible corpus snapshot matters.
- `-force` — delete and recreate a populated `cards` index.
- `-tarball-base URL` — source tarball base; defaults to the dataset's `codeload.github.com` path. Override it to seed from a mirror.

Run the checks the CI workflow runs:

```bash
go build ./...
go vet ./...
go vet -tags acceptance ./internal/...
go vet -tags relevance ./internal/relevance
go test -race ./...
golangci-lint run
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
find web -name '*.js' -exec node --check {} \;
node --test web/js/*.test.mjs
```

The frontend has no `package.json` and no dependencies, so Node runs both checks directly. The scripts are ES modules, which needs Node 20.19 or 22.7 or later; CI uses 24.

CI adds one job this list cannot express locally: it builds the runtime image and asserts via `docker inspect` that it does not run as root.

In full, `.github/workflows/ci.yml` runs three jobs on every pull request and on every push to `master` — a Go job that builds, vets, vets the tag-gated `internal/acceptance` and `internal/relevance` packages separately so neither can stop compiling unnoticed, runs the tests under `-race`, then runs golangci-lint at its pinned `v2.13.1` and `govulncheck` at the same pinned `v1.8.0` as above, so the scanner itself cannot change under CI; the vulnerability database is still fetched at run time, so a newly published advisory can still turn `master` red; a frontend job that runs `node --check` over every shipped `.js` file under `web/` and fails if that glob matches nothing, then runs `node --test web/js/*.test.mjs` and fails first if no test file matches; and the image job just described, whose assertion is stricter than its name — it fails if the configured user is empty as well as if it is `root`.

The acceptance suite is build-tagged and runs against a live stack:

```bash
POKESEARCH_URL=http://localhost:8080 go test -tags acceptance -count=1 ./internal/acceptance ./internal/apischema
```

The suite asserts that `/api/meta` reports a stamped build, a version other than `dev` and a commit other than `none`, so the stack under test has to be built with the `-ldflags` stamp described under *Build identity* below. A binary built without it fails that check. For a server built outside Docker, pass the same three `-X` flags the `Dockerfile` does, for `Version`, `Commit` and `Built` in `internal/version`.

`.github/workflows/acceptance.yml` runs that same suite end to end on demand (`workflow_dispatch`): it brings the stack up, seeds the pinned ref, asserts `/healthz` reports exactly 20,635 documents, and only then runs the tests.

### Build identity

Builds are stamped through `-ldflags` and reported by `/api/meta`. An unstamped build honestly reports `dev` / `none` / `unknown` rather than a misleading version:

```bash
VERSION=$(git describe --tags --always) \
COMMIT=$(git rev-parse --short HEAD) \
BUILT=$(date -u +%FT%TZ) \
docker compose up -d --build
```

## API

| Endpoint | Purpose | Parameters |
|---|---|---|
| `GET /` | The embedded frontend, served from `embed.FS` | — |
| `GET /livez` | Liveness only — never touches Elasticsearch. Container healthcheck target. | — |
| `GET /healthz` | Elasticsearch reachability and indexed document count | — |
| `GET /api/meta` | Build identity and corpus provenance | — |
| `GET /api/search` | Fuzzy multi-field card search, filters, facets, sorting, pagination | `q`, `id`, `supertype`, `types`, `set`, `rarity`, `series`, `hp_min`, `hp_max`, `sort`, `order`, `page`, `page_size`, `debug=1` or `debug=true` |
| `GET /api/suggest` | Deduplicated card-name completion with a fuzzy retry | `q` |
| `GET /api/explain` | Score anatomy for one card under one query: per-branch contributions | `id`, `q` (both required) |
| `GET /api/compare` | The same query ranked under two named weight profiles: both top-ten windows, the per-card movement and Spearman's ρ over their union | `q` (required), `a`, `b` — profile names from `served` \| `previous` \| `runner-up`, defaulting to `served` and `previous` |
| `GET /api/stats` | Corpus analytics: prints per year, HP distribution, type/class/rarity/series breakdowns, max HP | `debug=1` or `debug=true` |
| `GET /api/v1/cards` | Batch card lookup for API clients such as PokéPrices: the cards as indexed, plus the ids that matched nothing under `missing`. Optional API key in `X-Api-Key`. | `ids` (required): a comma list of at most 100 distinct card ids |
| `GET /api/v1/cards/{id}` | One card as indexed, with the corpus's seed provenance. Optional API key in `X-Api-Key`. `404` for an id no card has. | — |
| `GET /api/v1/sets` | Every set with its release date and `card_count`, oldest first. Optional API key in `X-Api-Key`. | — |
| `GET /debug/vars` | expvar counters. **Opt-in** — only exists when `METRICS=1` | — |

Routes are registered with method-scoped patterns, so a `POST` to a `GET` route returns `405`, not `404`. Under `/api/` both answers, and the `404` for a path no route has, are JSON error envelopes (see *Errors*); the `405` carries an `Allow` header.

The three `/api/v1/` routes are the versioned API for clients outside this repository; the reasons and what the version promises are in [ADR 12](docs/DECISIONS.md#adr-12--a-versioned-api-for-outside-clients). Every answer is `{"data": …, "seed": {…}}`, and the batch route adds `missing`. The key is optional: a request with no `X-Api-Key` header is anonymous and answered as before. A header that names no configured key is a `401` carrying `WWW-Authenticate: ApiKey realm="pokesearch", header="X-Api-Key"`, so a mistyped key is found out rather than ignored. The server reads its keys once, at start-up, from the comma list in the `POKESEARCH_API_KEYS` environment variable. v1 answers carry a weak `ETag` and `Cache-Control: no-cache`, so a conditional request is a `304`.

The JSON Schemas in [docs/api/schema/](docs/api/schema/) describe the answers of search, suggest, explain, compare, stats and meta, the three v1 routes and the error envelope. The acceptance suite checks live answers against them.

The embedded frontend's files carry weak `ETag`s as well: a conditional request for one is a `304`, `index.html` is served `no-cache`, and every other asset for five minutes with `must-revalidate`.

Search responses carry Elasticsearch's `took_ms`, the effective `page_size`, and live `supertype`, `types`, `rarity`, `set_series`, and readable `sets` facets. The `set` parameter takes an exact set ID; combine it with `q` to search within that set. Add `debug=1` (or `debug=true`) to receive the generated DSL in the response.

`hp_min` and `hp_max` match only cards that have an HP value, so any HP filter leaves out cards without one: every Energy card and nearly every Trainer. `hp_min=0` returns 17,476 of the 20,635 cards, not all of them.

A search that finds nothing may carry `did_you_mean`, a corrected spelling of `q`. It appears only on a zero-result text search, and only when the correction itself finds cards under the same filters: `q=abxx` suggests `abra`, but `q=abxx&supertype=trainer` offers nothing, because no Trainer card matches `abra`.

`GET /api/suggest` matches names without their accents: `q=pokemon` completes to `Pokémon Breeder`. Search itself does not fold accents, so `pokemon` and `pokémon` rank differently there.

`GET /healthz` returns `{"docs":N,"status":"ok"}`. Its shape and its Elasticsearch round-trip are a **frozen contract** — use `/livez` for cheap liveness.

`GET /api/meta` returns build identity plus corpus provenance:

```json
{
  "version": "v1.3.0",
  "commit": "4581829",
  "built": "2026-08-22T19:21:13Z",
  "docs": 20635,
  "seed": { "ref": "39a26a144c8b6ef6c2fb17b2c29d0bb7121e3a11", "seeded_at": "2026-08-22T19:40:00Z" },
  "request_id": "3f2a8c1d9e0b4a76"
}
```

`seed` is `null` for an index seeded before provenance stamping existed. That is reported, not repaired: the stamp appears on the index's next reseed.

### Corpus analytics

`GET /api/stats` aggregates the whole archive in a single `size: 0` request — a `date_histogram` of prints per year, a 30-point HP histogram, the four categorical breakdowns (read from the same facet registry the filter rail uses) and the corpus maximum HP. The corpus is immutable between reseeds, so the result is computed on the first request and served from memory afterwards; an empty index is served but never cached, so the first request after a seed heals it without a restart. A reseed of an index that already holds cards is not visible here until the app restarts; see [docs/OPERATIONS.md](docs/OPERATIONS.md#after-a-reseed). `took_ms` is therefore the Elasticsearch time of the aggregation that produced the payload, not of the request being answered.

The `#stats` view renders that payload in hand-rolled CSS charts — no charting library — and the observability rail stays attached, so the aggregation DSL is one click away from the numbers it produced:

![The #stats corpus-analytics view: prints per year, HP distribution and energy-type breakdowns, with the observability rail alongside](docs/media/stats.png)

### Relevance design

A text query becomes four scored `should` branches. The boosts encode an *ordering* — exact beats prefix beats typo beats card text — and a 27-point sweep over the judged set of [ADR 9](docs/DECISIONS.md#adr-9--how-relevance-is-evaluated) kept that ordering while halving two of the sizes inside it. The **Measured** column is what the sweep found, and [ADR 10](docs/DECISIONS.md#adr-10--measured-branch-weights) is the record:

| Branch | Query | Boost | Measured | What it is for |
|---|---|---|---|---|
| `exact` | `term` on `name.kw` | **8** | Inert from 4 to 16 — every metric identical to the last digit, so the sweep gives no reason to move it | An exact name always wins. Searching "Pikachu" must not rank a Pikachu-adjacent card first. |
| `prefix` | `multi_match` `bool_prefix` over `name.sayt` + 2/3-grams | **2** | Swept over 2 / 4 / 8; **2** won, halving the old 4 | Instant as-you-type matching, so partial names still rank highly. |
| `fuzzy-name` | `match` on `name`, `fuzziness: AUTO` | **1.5** | Swept over 1.5 / 3 / 6; **1.5** won, halving the old 3 | Typo tolerance — "pikuchu" finds Pikachu — ranked below a real prefix match. |
| `text` | `multi_match` `best_fields` over attack/ability names and text, flavor text, set name, artist | *implicit 1* | Not swept — it is the unit the other three are ratios of, so moving it would only rescale them | Discovery through card text: "flip a coin" finds cards by what they do. |

The numbers behind that column — the metric table overall and per stratum, where the judgments came from, the queries the adopted weights moved, and the command that reproduces the run — are published in [docs/RELEVANCE.md](docs/RELEVANCE.md).

The rest of that design — the branch badges and `GET /api/explain`, highlighting and `did_you_mean`, scoring-neutral filters and disjunctive facets, the stock-BM25 and analysis-chain reasoning, and the measured shadow-analyzer index of [ADR 11](docs/DECISIONS.md#adr-11--a-shadow-analyzer-index-measured-not-adopted) — moved to [docs/RELEVANCE.md](docs/RELEVANCE.md) under *Design notes moved from the README*.

### Paging

`page_size` is bounded **1–100** and defaults to **24**. Pagination is capped at a reachable window of **9,600 documents** so `from + size` always stays inside Elasticsearch's 10,000-result window:

```text
pages = ceil(min(total, 9600) / page_size)
```

`pages` is therefore deliberately **not** `total / page_size` on large result sets: a browse of all 20,635 cards reports `pages: 400` at the default size, `96` at `page_size=100`, and `9600` at `page_size=1`. Out-of-range values clamp rather than fail — `page_size=500` becomes `100`, `page=999999` becomes the last reachable page.

### Errors

Every non-2xx **JSON API** response uses one shape: an `error` object carrying `code`, the offending `field` where there is one, and a `message`, beside the request's `request_id`. The example body and the reasoning behind the contract are [ADR 7](docs/DECISIONS.md#adr-7--a-strictlenient-error-contract).

| Code | Status | Meaning |
|---|---|---|
| `invalid_param` | `400` | A strict parameter was supplied with an invalid value, or a required one (`q` on `/api/explain` and `/api/compare`, `id` on `/api/explain`, `ids` on `/api/v1/cards`) was omitted. `field` names it. Also returned, with no `field`, when Elasticsearch rejects the query a search, suggest, explain or compare request produced: the cluster answered and the input could not be run, so it is the client's `400` and not an outage. The cluster's reason goes to the log, never to the client. |
| `unauthorized` | `401` | An `X-Api-Key` header on an `/api/v1/` route names no configured key. The answer carries `WWW-Authenticate`. |
| `not_found` | `404` | No endpoint has the path under `/api/`, or no card has the id on `GET /api/v1/cards/{id}` (then with `field` set to `id`). |
| `method_not_allowed` | `405` | The `/api/` path exists but not for that method. The `Allow` header lists the methods it answers. |
| `es_unavailable` | `503` | Elasticsearch could not be reached, the index is missing, or the cluster failed in any way other than rejecting the query. The cause — including a truncated Elasticsearch error body — goes to the log, never to the client. The `/api/v1/` routes answer every Elasticsearch failure this way. It is not the only `503`: `overloaded` below is a busy server, not a failed cluster. |
| `overloaded` | `503` | The server was already handling as many requests as it admits at once (`MAX_IN_FLIGHT`, 128 by default), so this one was refused at once, without waiting, without its parameters being read and without reaching Elasticsearch. The answer carries `Retry-After: 1` and `Connection: close`: try again after a second. Outside `/api/` the same refusal is a plain-text `503`. `GET /livez` and `GET /healthz` are never refused. |

The Elasticsearch client retries a failed call once, after 100 ms, when the cluster cannot be reached or answers `502` or `504`. Any other answer, `503` included, fails at once. A dead cluster therefore costs a request about a tenth of a second.

Parameters split into three groups:

| Behaviour | Parameters | On invalid input |
|---|---|---|
| **Strict** | `sort`, `order`, `supertype`, `hp_min`, `hp_max`, `page`, `page_size`, the length of `q`, and the size of `rarity`, `series`, `set` and `id` | `400` with the offending `field`. Rejected before Elasticsearch is called. |
| **Lenient** | members of the `types` comma-list; unknown query keys | Dropped or ignored, `200`. |
| **Kept as given** | members of the `rarity` and `series` comma-lists; `set` | An unknown value is kept and matches nothing, so a request that names only unknown values is a `200` with a total of `0`. It is not dropped, so a typo cannot widen the answer. |

`sort` and `order` are trimmed but case-sensitive: `sort=hp` works, `sort=HP` is a `400`. `q` may be at most 256 characters and 32 terms, on `/api/search`, `/api/suggest`, `/api/explain` and `/api/compare`; over either limit is a `400` with `field` set to `q`. Both are counted after trimming. A character is a Unicode code point, not a byte, so 256 `é` pass and 257 fail, and a term is a run of non-space characters. On `/api/search`, `rarity` and `series` take at most 64 values each, totalling at most 1,024 characters (both counted before repeats are dropped), and each of their values, `set` and `id` at most 128 characters; over a limit is a `400` with `field` naming the parameter.

**A repeated parameter takes its first value:** `?page_size=1&page_size=50` is `page_size=1`.

Some spellings are accepted as the same value:

- `debug=true` is `debug=1`. Any other value leaves it off.
- `types=Steel` is `Metal` and `types=Normal` is `Colorless`, the names the page shows, in any letter case. The API still returns the source dataset's names.
- `supertype` is case-insensitive and takes `Pokémon` with its accent as well as `pokemon`.
- A `rarity` that differs from a stored value only in letter case or spacing is mapped onto it: `rarity=common` returns the same 5,407 cards as `Common`. The one exception is `Rare Holo EX` and `Rare Holo ex`, which differ only in case and so stay exact.
- `series` and `set` are matched exactly: `series=sword & shield` and `set=BASE1` match nothing.

Out-of-range integers that parse are clamped, not rejected — a clamp is a contract, an alphabetic `page` is a typo. `page_size=500` becomes `100`, `page=-5` becomes `1`. A value that is not an integer, or is too large for a 64-bit integer (a 20-digit one), is a `400`. `GET /api/suggest` reads only `q`, so errors on its other parameters are ignored; the one it can return is the `q` length. The two responses that sit outside the envelope by design, `/healthz` answering a failed probe and the static file handler's plain-text answers to paths outside `/api/`, are described in [ADR 7](docs/DECISIONS.md#adr-7--a-strictlenient-error-contract) with the rest of the rationale. The plain-text `overloaded` refusal of a path outside `/api/` is the third, recorded under [ADR 8](docs/DECISIONS.md#adr-8--what-was-deliberately-not-built).

### Type names

The API and index retain the source dataset's canonical TCG type values. The interface presents `Metal` as **Steel** and `Colorless` as **Normal**, including filter labels, active-filter chips, attack costs, card details, and the `#stats` chart labels.

### Card images

Card art is served from `images.scrydex.com`. The index and the API keep the source dataset's original `images.pokemontcg.io` URLs verbatim; the **frontend** rewrites them to Scrydex card-ID routes at render time, because the original URLs return real 404s. Keeping the rewrite at the presentation layer means the stored documents stay faithful to their source and a future art host is a one-function change. This is a hard third-party dependency: if Scrydex is unavailable, art fails to load while search itself continues to work. What Scrydex's public documentation says about using its images, and what it leaves open, is recorded in [ADR 13](docs/DECISIONS.md#adr-13--card-images-are-hot-linked-from-scrydex).

## Observability

Every request gets an ID and leaves a trail that connects the browser to the log:

- **`X-Request-Id` on every response.** An inbound Cloudflare `Cf-Ray` is honoured first and an inbound `X-Request-Id` second, so one trace spans edge, app and client and a client cannot shadow the edge's ID; otherwise one is generated. An inbound value that does not look like a trace ID is replaced rather than sanitized — it ends up in a response header and in every log line — and a `Cf-Ray` that does not look like one falls through to the `X-Request-Id`.
- **One access line per request:** `method`, `path`, `status`, `bytes`, `dur_ms`, `request_id`.
- **One query line per search or suggestion** that reaches Elasticsearch, carrying the full generated DSL — the same query the UI's inspector shows. Both lines are JSON, both use UTC millisecond timestamps, and both name the same `request_id`, so correlating them never involves reasoning about time zones.
- **`METRICS=1`** publishes stdlib `expvar` counters at `/debug/vars`, keyed by route and status (`search_200`, `search_400`, …). Counting is always on; publishing is opt-in, because the production reverse proxy forwards whatever path it is given. **Never set `METRICS=1` in the server topology.**

**[Courier](https://github.com/AndresThePerez/Courier)** — the companion API test runner and load tester, [live at courier.andrestheperez.com](https://courier.andrestheperez.com) — drives this API on the deploy host over the internal container network, so the knee it reports is a property of **this** service rather than of the tool: throughput bends at about **10 workers**, **154.1 req/s** at a p50 of **70.36 ms** and a p95 of **118.80 ms**, **0.00%** errors, against a p95 target of **150 ms**. Past that point the extra concurrency queues rather than works, and the error rate never leaves zero. The rail publishes two budgets — ES query under **100 ms**, UI response under **250 ms** — as stated targets, and the companion's runs are the evidence they hold.

## Architecture

```mermaid
flowchart TD
    B["Browser<br/>vanilla ES modules, zero innerHTML"]
    S["Go server — cmd/server<br/>API + embedded frontend"]
    Q["internal/search<br/>pure DSL builders, golden-tested"]
    E["Elasticsearch 8.19<br/>single node, one shard"]
    V[("es-data volume")]
    D["cmd/seed<br/>one-shot, --profile seed"]
    G["pokemon-tcg-data tarball<br/>streamed from GitHub"]

    B -- "HTTP :8080" --> S
    S -- "params" --> Q
    Q -- "map[string]any DSL" --> S
    S -- "Compose-internal :9200" --> E
    E --- V
    G -- "in-memory transform" --> D
    D -- "_bulk, 1000-doc chunks" --> E
```

The production Compose topology publishes only the Go application. `docker-compose.dev.yml` is deliberately opt-in so a plain `docker compose up` never exposes Elasticsearch on the host. A plain-text rendering of the same topology is in [docs/OPERATIONS.md](docs/OPERATIONS.md).

Elasticsearch runs `docker.elastic.co/elasticsearch/elasticsearch:8.19.24` as a single node with a 512m JVM heap. The runtime image is `gcr.io/distroless/static-debian12:nonroot` — no shell, no package manager, running as uid 65532, with both build and runtime base images pinned by digest. Because there is no shell, the server binary is its own healthcheck client (`/server -ping`).

**Why any of this is the way it is:** [docs/DECISIONS.md](docs/DECISIONS.md).

## Deployment

`docker-compose.yml` is the whole stack; layering `docker-compose.server.yml` on top of it adds `restart: unless-stopped` and per-service memory caps for a host that runs other tenants, and gives the app `GOMEMLIMIT=180MiB` under its `256m` cap. With the cap on requests in flight (`MAX_IN_FLIGHT`), that ties the app's memory mostly to the requests it works on rather than to how many arrive (a refused one costs its connection only until the `503` is sent); telling clients apart and rate-limiting them is left to the proxy in front.

Deploy on any Docker host behind a TLS-terminating reverse proxy; the public instance at <https://pokesearch.andrestheperez.com> sits behind one. Elasticsearch stays on the Compose-internal network with no host port in any topology, so only the Go application is ever reachable.

Never edit the deployed clone in place: commit on a workstation, push, pull on the host.

The host runbook — the deploy variables, the pinned-ref seed, backup, restore and the recovery ladder — is [docs/OPERATIONS.md](docs/OPERATIONS.md).

## Author

Built and hosted by [Andres Perez](https://andrestheperez.com), a senior backend engineer focused on API design and platform services in Go and PHP. PokéSearch is the Elasticsearch relevance project I keep public, and [Courier](https://github.com/AndresThePerez/Courier) is the API tooling I test it with; the [portfolio](https://andrestheperez.com) and [resume](https://andrestheperez.com/resume.html) carry the rest.

## License

[MIT](LICENSE) © 2026 Andres Perez.

Pokémon and Pokémon character names are trademarks of Nintendo, Creatures Inc., and GAME FREAK inc. Card data comes from the `pokemon-tcg-data` dataset. This project is unaffiliated with those companies and is non-commercial.
