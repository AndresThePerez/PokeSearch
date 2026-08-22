# Architecture decision records

Why Pokesearch is built the way it is. Each record states the decision, the
forces behind it, and — the part that matters most — what it costs. Records are
append-only: a superseded decision keeps its entry and gains a pointer forward.

| # | Decision | Status |
|---|---|---|
| [1](#adr-1--separate-seed-and-server-binaries-in-one-image) | Separate seed and server binaries in one image | Accepted |
| [2](#adr-2--internalsearch-is-a-pure-dsl-builder) | `internal/search` is a pure DSL builder | Accepted |
| [3](#adr-3--disjunctive-facets-via-post_filter) | Disjunctive facets via `post_filter` | Accepted (remediation 2026-07-11) |
| [4](#adr-4--buildless-frontend-embedded-with-embedfs) | Buildless frontend embedded with `embed.FS` | Accepted |
| [5](#adr-5--distroless-nonroot-digest-pinned-runtime-image) | Distroless, nonroot, digest-pinned runtime image | Accepted |
| [6](#adr-6--the-set-catalog-is-cached-separately-from-hot-search) | The set catalog is cached separately from hot search | Accepted (bug fixed 2026-08-22) |
| [7](#adr-7--a-strictlenient-error-contract) | A strict/lenient error contract | Accepted — supersedes drop-don't-reject |
| [8](#adr-8--what-was-deliberately-not-built) | What was deliberately not built | Accepted |

---

## ADR 1 — Separate seed and server binaries in one image

**Decision.** Ingestion and serving are two commands (`cmd/seed`, `cmd/server`)
shipped in one image. The server never downloads data, never talks to GitHub,
and never writes to the index. Seeding is an explicit operational action, run
through a Compose profile so a normal `docker compose up` cannot trigger it.

**Why.** An ingestion failure — a GitHub outage, a malformed upstream file, an
OOM during a bulk load — must not be able to affect a search request. Splitting
the binaries makes that structural rather than a matter of care. Shipping both
in one image keeps deployment to a single artifact.

**Cost.** Updating the corpus is a deliberate ops step, not something that
happens on deploy. That is the intended trade, but it is a trade.

**Shape of the pipeline.** Tarball streamed from GitHub → transformed in memory
→ `_bulk` in 1,000-document chunks with `refresh_interval: -1` → refresh
restored → `_forcemerge` to one segment → `_count` verified against the number
of documents built. Nothing is written to disk at any point.

---

## ADR 2 — `internal/search` is a pure DSL builder

**Decision.** `BuildQuery`, `BuildSuggest` and friends are pure functions
returning `map[string]any`. They import no Elasticsearch client. The client
library only transports what they produce.

**Why.** One decision buys three properties at once. The generated DSL can be
asserted **byte for byte** in unit tests, written to the log as a replayable
JSON line, and returned to the browser for the observability rail — all without
a cluster. Relevance logic becomes something you can diff in a pull request.

**Cost.** Query construction is untyped (`map[string]any`), so a typo in a field
name is caught by the golden tests rather than by the compiler. That is the
trade the golden tests exist to cover.

**Evidence it works.** Twice during Milestone 3 a change was predicted to alter
the generated DSL and did not: adding field-error reporting to `ParseParams`
(`2ac01b4`) and threading `page_size` through the builder (`23a99fa`). In both
cases the golden files were byte-identical and the tests said so immediately —
verified rather than assumed.

---

## ADR 3 — Disjunctive facets via `post_filter`

**Decision.** The text query stays top level. The complete set of active filters
moves to `post_filter`, so it narrows hits without narrowing aggregations. Each
facet is then wrapped in a `filter` aggregation containing every active filter
**except its own**.

**What a facet count means.** "Cards available for this value once the text
query and every *other* active filter are applied." Concretely: the number shown
next to an option predicts what happens when you click it.

**Why it was rewritten (2026-07-11, `7a6e6b5`).** The original implementation
applied one filter set to hits and aggregations together, and gave the `sets`
facet a `global` aggregation. Four defects followed, all confirmed against the
live index:

| Symptom | Root cause |
|---|---|
| `q=Pikachu` returned 221 cards and narrowed every facet — except Set, which still showed all 173 corpus-wide counts | `sets` used a `global` aggregation while the other four ran inside the filtered query |
| Selecting *Rare* cut the rarity dropdown from 31 options to `All + Rare` | a facet's own filter was applied to its own terms aggregation |
| With Fire selected, Water showed 6 — but clicking Water moved the total from 1,569 to 3,992 | own-facet filtering produces co-occurrence counts, not available-option counts |
| `?set=base1&rarity=Promo` returned 0 results, kept both URL params, yet silently cleared the rarity control | the frontend's option renderer mutated state when the selected bucket was absent |

A fifth, separate defect: `terms.size` for rarity was 30, taken from a design-time
estimate of "~30". The live corpus has **38** rarities, so 8 were unreachable.
Multi-shard `terms` approximation was ruled out explicitly — the deployment runs
a single shard.

**Cost.** The aggregation DSL is substantially more complex than one global
filter, and each facet's exclusion list has to stay correct as parameters are
added. The acceptance suite exists largely to hold this in place: it locks the
cardinalities (3 supertypes, 11 types, 38 rarities, 17 series, 173 sets) and
asserts that a facet's bucket count equals the direct query total for that value
under the same scope.

---

## ADR 4 — Buildless frontend embedded with `embed.FS`

**Decision.** HTML, CSS and vanilla JavaScript are embedded into the binary with
`go:embed`. No bundler, no npm, no build step. The frontend never uses
`innerHTML` — every dynamic node is constructed and given `textContent`.

**Why.** One artifact serves the API and the UI, with no second toolchain to
install, version or keep current. The zero-`innerHTML` rule makes the UI
XSS-free by construction rather than by review.

**Cost, and how it is paid.** A bundler would catch a reference to an element ID
that no longer exists. Without one, a renamed ID breaks the page *silently* —
there is no build to fail. `web/embed_test.go` is the substitute: it reads
`index.html` and `app.js` **through the embedded FS** and asserts in both
directions — every ID the script wires up exists in the markup, and every ID the
markup declares is referenced by the script. Its failure message is the point:
`app.js never references %q; markup and script are out of sync`.

**Known dependency.** Card images are rewritten to `images.scrydex.com` routes,
after real upstream 404s were found on the original `images.pokemontcg.io` URLs.
That is a hard third-party dependency and is documented as one in the README.

---

## ADR 5 — Distroless, nonroot, digest-pinned runtime image

**Decision.** Multi-stage build: `golang:1.26` compiles two CGO-free binaries,
`gcr.io/distroless/static-debian12:nonroot` runs them as uid 65532. Both bases
are pinned by digest, not by tag.

**Why.** The runtime image contains no shell, no package manager and no libc —
there is nothing to exploit and nothing to patch. `static` is sufficient
because neither binary writes to disk: the seeder streams its tarball through
memory, and the server only reads its embedded filesystem. Digest pinning is
what makes "it built last week" keep meaning something; a tag is a moving
pointer. Refresh them deliberately with `docker buildx imagetools inspect`.

**Cost.** No shell means no `curl` for a container healthcheck. The server
binary therefore carries a `-ping` mode and acts as its own healthcheck client.
That is a real cost of the decision, paid in about fifteen lines of `main.go`.

**Related invariant.** Elasticsearch has no host port in any topology except the
opt-in `docker-compose.dev.yml`. Isolation is enforced by file layout, and it is
verified rather than assumed.

---

## ADR 6 — The set catalog is cached separately from hot search

**Decision.** Set labels and release dates — which are immutable — are fetched
once by a standalone `size: 0` request and cached for the process lifetime.
Per-request set *counts* are merged onto that catalog on every search.

**Why.** All 173 sets must stay visible and selectable even when the current
query matches none of them, so the UI never drops an option. Getting labels
requires a `top_hits` sub-aggregation, which is expensive — and it was running
on *every* search. Splitting it out removes `top_hits` from the hot path, and
the standalone request is additionally eligible for Elasticsearch's request
cache, while hot searches compute only the dynamic counts.

**Cost.** The cache refreshes only on restart. Since the corpus is immutable
between seeds, cache invalidation is "restart after reseed" — which is already
the standing operational policy.

**The bug, and the fix (2026-08-22, `b22d050`).** The original cache stored a
zero-length non-nil slice, which defeated its own `!= nil` guard. A server
started **before** the index was seeded — the exact ordering in the README's
quick start — cached the empty catalog permanently and served an empty sets
facet until someone restarted it. The same function also held its mutex across
the Elasticsearch round trip, serializing every cold-start request behind one
uncancellable call.

The fix keeps the standard library only — no `singleflight`, no new dependency:

- `sync.RWMutex`, read under `RLock`;
- the Elasticsearch call runs **outside** any lock, so a slow cluster cannot
  serialize cold starts;
- an empty catalog is served but **never cached**, so the first search after
  seeding heals itself;
- a double check before storing.

Concurrent cold starts may fetch redundantly. That is bounded, harmless, and
the price of not adding a dependency. The same shape was later reused for
`/api/meta`'s seed-provenance cache, for the same reasons.

---

## ADR 7 — A strict/lenient error contract

**Status.** Accepted 2026-08-22. **Supersedes** the original rule, which was
stated in the Milestone 1 design spec as *invalid values are dropped, never
rejected*.

**What was wrong with dropping.** The entire error vocabulary was
`503 {"error":"elasticsearch unavailable"}`. Invalid input was silently
ignored: `sort=bogus` fell back to a default, `types=Wizard` was dropped. The
consequence is the reason this changed — **a typo'd filter returned a plausible
wrong answer**, with a `200` and no indication anything had been ignored.
Separately, Elasticsearch error bodies were read and discarded, which made a
mapping error and a down cluster indistinguishable in the log.

**The decision.** One error shape everywhere:

```json
{
  "error": { "code": "invalid_param", "field": "sort", "message": "sort must be one of relevance|newest|oldest|hp|name" },
  "request_id": "3f2a8c1d9e0b4a76"
}
```

- **Strict → `400`:** `sort`, `order`, `supertype`, and non-numeric `hp_min`,
  `hp_max`, `page`, `page_size`. Rejected before Elasticsearch is called — a
  typo must not cost a cluster round trip.
- **Lenient → `200`:** unknown members inside the `types` / `rarity` / `series`
  comma-lists, and unknown query keys entirely.
- **Out-of-range numerics still clamp.** The distinction is deliberate: *a clamp
  is a contract, an alphabetic page is a typo.*
- Elasticsearch error bodies are logged (truncated to 2KB) before the client
  receives its generic 503. The cause reaches the operator; it never reaches
  the client.

**Cost.** This is the milestone's one breaking change. It was made while the
only two consumers were the embedded UI and a not-yet-written test suite —
which is the cheapest this contract will ever be to change.

**Errors carry a request ID.** Every response also sets an `X-Request-Id`
header, honouring an inbound `X-Request-Id` or Cloudflare's `Cf-Ray` when
present. The same id appears in the access log line and in the query log line,
so a user reporting an id lands an operator on the exact DSL that answered it.
An inbound id that does not look like a trace id is replaced rather than
repaired — it is echoed into a header and into logs, so it is validated first.

---

## ADR 8 — What was deliberately not built

Recorded so the absences read as decisions rather than oversights.

**App-level rate limiting.** Cloudflare fronts the public URL, and the project's
companion load generator is the intended internal traffic source. A limiter
would return `429` to the very tool built to exercise the API. Revisit only if
organic traffic ever warrants edge rules.

**URL versioning (`/api/v1/…`).** Two consumers exist, both co-versioned with
the server. A version segment would be ceremony without a second independent
client. New endpoints and parameters are added additively to the existing
`/api/*` surface instead.

**`search_after` deep pagination.** Offset pagination is capped at a reachable
window of 9,600 documents, inside Elasticsearch's 10,000-result limit. That
covers realistic browse depth; the divergence between `pages` and
`total / page_size` is documented in the README rather than engineered away.

**Elasticsearch HA / replicas.** Single node, one shard, zero replicas,
refresh disabled during bulk load, force-merged afterwards. That is the correct
tuning for a small immutable corpus and an honest fit for a portfolio
deployment. It is explicitly not designed for continuous indexing.

**A frontend framework or bundler.** See ADR 4 — the buildless embed is a
feature, not a gap. Native ES modules are the ceiling; charts are hand-rolled
CSS and SVG for the same reason.

**Semantic / vector search.** A deferred non-goal rather than a rejected one:
`dense_vector` on a single node with a 512 MB heap is a science project, not a
feature. It stays on the stretch list.

**Re-seeding to a newer corpus.** Out of scope until the companion project
ships. Because its fixtures assert exact corpus cardinalities, a reseed is a
coordinated two-project change, not a unilateral one.
