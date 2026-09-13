# Architecture decision records

Why PokéSearch is built the way it is. Each record states the decision, the
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
| [9](#adr-9--how-relevance-is-evaluated) | How relevance is evaluated | Accepted |
| [10](#adr-10--measured-branch-weights) | Measured branch weights | Accepted |
| [11](#adr-11--a-shadow-analyzer-index-measured-not-adopted) | A shadow analyzer index, measured not adopted | Accepted |

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

**Two edge cases the envelope does not cover.** The first is a pair of responses
that are not JSON envelopes at all; the second is how the `types`, `rarity` and
`series` filters treat case. Both paragraphs moved here verbatim from the README.

Two responses sit outside the envelope by design: `/healthz` answers a failed probe with its own frozen `{"status":"error"}` body, because a health endpoint's shape must never change, and the static file handler returns net/http's plain-text `404` for unknown asset paths.

Case is handled unevenly across those filters, and that is a known asymmetry rather than a design. `supertype` is lower-cased before it is matched against `pokemon|trainer|energy`, and `types` members are compared case-insensitively against the eleven canonical type names — but `rarity` and `series` members are only trimmed and then matched verbatim against bare keyword fields that carry no normalizer. So `rarity=common` answers `200` with a total of **0**, while `rarity=Common` returns **5,297**. Send those two the way the facets hand them back: title-case rarities (`Common`, `Uncommon`, `Rare Holo`) and full series names (`Sword & Shield`, `Scarlet & Violet`). Treat that as the interim contract — closing the gap either changes observable filter semantics or means a mapping change and therefore a reseed, so it is written down here rather than quietly altered.

---

## ADR 8 — What was deliberately not built

Recorded so the absences read as decisions rather than oversights.

**App-level rate limiting.** Cloudflare fronts the public URL, and the project's
companion load generator, [Courier](https://github.com/AndresThePerez/Courier),
is the intended internal traffic source. A limiter would return `429` to the
very tool built to exercise the API. Revisit only if organic traffic ever
warrants edge rules.

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

**Semantic / vector search.** A deferred non-goal rather than a rejected one,
and deferred with a design rather than a shrug. What would be built: one text
blob per card — name, attack and ability names and text, and `flavor_text`,
concatenated — embedded by a small sentence-embedding model, for example a
MiniLM-class encoder at 384 dimensions, into a single `dense_vector`. That
vector lives on a side index, `cards_vec`, keyed by card id and never as a
field on the cards index, so nothing that asserts the corpus's cardinalities
has to move: the cards mapping, its document count and every fixture built on
them stay exactly as they are. Retrieval then runs two branches — the existing
lexical `text` query and a `knn` search over the side index — merged by
reciprocal rank fusion in application code rather than by a retriever at the
engine. Fusing in Go keeps the merge diffable in a pull request like every
other ranking decision here, and sidesteps the question of which retriever
tiers a basic-license single node is entitled to. The bar is ADR 9's harness:
that lexical branch is the baseline, and a hybrid that does not beat it there
does not ship.

The arithmetic is why it waits. 20,324 documents at 384 dimensions and 4 bytes
per dimension is roughly 31 MB of raw vectors — and that figure excludes the
approximate-nearest-neighbor graph, which is the part that has to stay resident
for the search to be fast, on a node with a 512 MB heap. None of that has been
measured: the deferral rests on an estimate rather than a benchmark, which is
why this stays on the stretch list rather than in the backlog.

**Re-seeding to a newer corpus.** Out of scope until the companion project,
[Courier](https://github.com/AndresThePerez/Courier), ships. Because its
fixtures assert exact corpus cardinalities, a reseed is a coordinated
two-project change, not a unilateral one.

---

## ADR 9 — How relevance is evaluated

**Decision.** Ranking changes are judged against a fixed judgment set with
graded relevance, scored offline by a harness that builds every query it sends
through the same `internal/search` functions the server calls. A change to the
boosts ships on what that harness reports, not on how the first page of one
query looked to the person who made the change.

**Why.** Every boost in this repository is an *intended* ordering: name above
attack text, exact above prefix, weights chosen because they read as sensible
and never checked against anything. The gap that matters is not that they might
be wrong — it is that nothing in the repository can tell a ranking change that
helped from one that hurt. Spot-checking a few queries in the browser finds the
regression you thought to look for and misses the rest. A judgment set is what
makes ranking something that can fail a test.

**The judgment set.** Roughly 50 to 70 queries, grouped into named strata:
exact name, prefix, typo, attack text, artist, set name, and natural-language
intent. Each stratum is a distinct failure mode, so a metric that moves can be
attributed to one of them rather than averaged into invisibility — a typo fix
that quietly costs artist search is exactly the trade this is meant to expose.
Each query lists the documents judged for it on a graded scale of 0 to 3, from
irrelevant, through defensible, to the card the searcher plainly meant. Graded
rather than binary because relevance here is not binary: a different print of
the right card is not the answer, but it is not noise either.

**Every judgment carries a reason.** One line, stored beside the grade, saying
why that document earned it. That is what makes the set auditable rather than
asserted: a disagreement later is then a conversation about a stated reason
instead of an argument about a number somebody typed. It is also the honest way
to record that these are judgments and not facts.

**The metrics.** nDCG@10 is the headline, with precision@10 and MRR reported
alongside it. nDCG earns the headline because it is the metric that reads both
halves of what the judgments say — the grade of a hit *and* where the hit
landed — so moving a grade-3 card from the bottom of the window to the top is
visible in it, while precision@10 counts hits in the window and can see neither
the grade nor the order. Precision@10 stays because it is the one number a
reader interprets correctly at a glance. MRR stays for the exact-name and
prefix strata, where there is a single right answer and its rank is the whole
story.

**It reuses the served query builder.** The harness calls `BuildQuery` and
sends what it returns; it does not restate the DSL in a fixture of its own.
This is a fourth thing ADR 2's purity buys — beyond the three its own "Why"
lists. An evaluation of a query the
server does not issue measures nothing, and the only durable defence against
that drift is for there to be one builder rather than two. The intended shape
follows from that: a build-tagged package under `internal/relevance`, so a
plain `go test ./...` stays cluster-free; the judgments checked in as JSON
beside it; an Elasticsearch `_rank_eval` request per metric against the `cards`
index, reported per stratum by grouping the per-query details client-side; a
baseline written under `docs/relevance/` recording the index, the
document count and the build it was measured on; and the written report in
`docs/RELEVANCE.md`. The acceptance workflow gates on that baseline on demand
and against a stated tolerance, rather than on every push — an evaluation needs
a seeded cluster, which a unit test deliberately does not.

**None of this is measured yet.** As of this record the harness does not exist,
no judgment has been written, and the boosts remain what they have always been:
an intended ordering. This ADR fixes the method so that the first numbers, when
they arrive, mean something. It reports none, and any relevance figure quoted
before the harness runs is a guess wearing a decimal point.

**Cost.** A judgment set is an opinion with a timestamp. It has to be
maintained — a reseed to a newer corpus can invalidate judgments that name
documents which moved or vanished — and it encodes one person's view of what a
good result is for this corpus, which is not the same thing as what a searcher
wanted. A metric that moves is only as trustworthy as the judgments underneath
it. The reasons are recorded so that trustworthiness can be argued with rather
than assumed.

---

## ADR 10 — Measured branch weights

**Decision.** The `prefix` boost is **2** and the `fuzzy-name` boost is **1.5**,
down from 4 and 3. The `exact` boost stays at **8** and the `text` branch keeps
Elasticsearch's implicit **1**. The two that moved were chosen by the harness
[ADR 9](#adr-9--how-relevance-is-evaluated) specified, against the judged set it
specified, on the metric it named the headline — and then re-measured on a
re-pooled set after the first measurement turned out to rest on a half-judged
window. The second measurement is the one recorded here.

**The grid.** Twenty-seven points: each swept boost took half its value, its
value, and double it — `exact` over 4 / 8 / 16, `prefix` over 2 / 4 / 8,
`fuzzy-name` over 1.5 / 3 / 6 — with the `text` branch held at 1 throughout. A
boost only ever sets a ratio, against the other branches and above all against
text's fixed 1, so the grid moves on a log scale; text is not swept because
moving it would rescale the other three rather than say anything new. Every
point is scored on all 50 judged queries with all three metrics. The harness
builds each request through `BuildQuery` and then overwrites the boosts of the
named `should` clauses, so a grid point is the served query with three numbers
rewritten and nothing else. `internal/search` gained no knob for this: [ADR
2](#adr-2--internalsearch-is-a-pure-dsl-builder) keeps it a pure builder, and a
one-off measurement is not a reason to grow a configuration surface.

**The reference point is 8 / 4 / 3.** Every "before" figure below is that point
— the weights served before this work — and not whatever happens to be served
when the sweep is next run. The harness names it as a constant and asserts it is
in the grid, because a delta measured against the current weights collapses to
zero the moment a point is adopted, and a report of no movement is not the same
as a report of no change.

**A point can only be adopted if its window is fully judged.** `unrated@10`
counts hits inside the evaluated window that no judgment covers, and such hits
score as irrelevant — not because anybody looked and judged them irrelevant, but
because nobody looked. A point carrying them has not been measured against the
pool at all, so it cannot be adopted at any score; it is a request to judge what
it surfaced and run again. The harness enforces this: the best point is reported
whatever it scores, and the run fails with `RE-POOL REQUIRED` if that point is
not fully judged.

That rule is written here because this decision violated it once. The first
sweep ran against a pool drawn from the reference point's own windows, so the
reference scored `unrated@10` 0 by construction and every challenger paid a toll
for surfacing cards nobody had graded — 8/2/1.5 carried 69 unjudged hits out of
a 500-slot window, 13.8% of it. It won anyway, and the temptation was to adopt
it on the argument that the bias ran in its favour. The judgment set was
re-pooled instead: 117 judgments were added so that every card any of the 27
points ranks in a top ten is graded, taking the set from 652 judgments to 769.
`unrated@10` is now **0 at all 27 points, in all seven strata**, and every number
below is measured on that pool.

**The result.** Overall nDCG@10 rises from **0.585617** to **0.645359**,
**+0.059743**. Precision@10 rises from 0.642000 to 0.662000. MRR@10 rises from
0.547167 to 0.603333. On the re-pooled set the adopted point improves all three,
which the half-judged measurement did not show — it had precision@10 falling.

| Stratum | nDCG@10 at 8/4/3 | at 8/2/1.5 | Δ |
|---|---|---|---|
| exact name | 0.992428 | 0.971654 | −0.020774 |
| prefix | 0.546714 | 0.569717 | +0.023003 |
| typo | 0.908825 | 0.876376 | −0.032449 |
| attack text | 0.478453 | 0.845457 | **+0.367004** |
| artist | 0.795661 | 0.795661 | 0.000000 |
| set name | 0.255307 | 0.327315 | +0.072008 |
| natural-language intent | 0.179890 | 0.195589 | +0.015699 |

The whole gain is one trade: the name branches were loud enough to drown the
card-text branch, and quieting them let card text be heard. Five of seven strata
improve, artist does not move at all, and exact name and typo each give up a
little.

**The exact boost is inert, and is therefore not a measured weight.** Every
metric, overall and per stratum, comes back identical to the last digit of a
float64 at `exact` 4, 8 and 16 — 4/2/1.5 and 16/2/1.5 tie the adopted point
exactly, and three of the twenty-seven points describe one ranking. That is what
a `term` clause does: it either fires for a card or it does not, and 4 already
lifts every card it fires for clear of everything else, so multiplying it
reorders nothing. The tie rule is that an exact tie leaves the current value in
place, so `exact` stays at 8 — because nothing measured argues against it, not
because anything measured earned it. This record says so rather than letting the
README table imply the number was won.

**The runner-up** is **8/4/1.5** at nDCG@10 **0.641568**, which keeps `prefix` at
4 and only halves `fuzzy-name`. It loses the headline by **0.003791** and costs
one fewer query than the adopted point. It is not the best point on every
metric — precision@10 across the grid peaks at 0.682000 at `prefix` 8 with
`fuzzy-name` 1.5, above both the adopted point's 0.662000 and the runner-up's
0.676000 — which is the ordinary situation when three metrics are reported and
one of them decides. A reader reopening this decision should start at 8/4/1.5:
the margin is thin enough that a differently pooled set could reverse it.

**The queries that got worse.** Six of fifty, against eleven better and
thirty-three unchanged:

| Query | Stratum | nDCG@10 | Δ |
|---|---|---|---|
| `pikchu` | typo | 0.498300 → 0.278432 | −0.219868 |
| `char` | prefix | 0.172290 → 0.000000 | −0.172290 |
| `dragoni` | prefix | 0.641046 → 0.505360 | −0.135685 |
| `professor oak` | exact name | 1.000000 → 0.865207 | −0.134793 |
| `bill` | exact name | 0.946994 → 0.936371 | −0.010623 |
| `mewtoo` | typo | 0.863471 → 0.856201 | −0.007271 |

Against those, the eleven that improved, largest first: `energy burn` 0.000000 →
1.000000, `rain dance` 0.000000 → 0.618131, `pika` 0.531000 → 1.000000,
`thunder jolt` 0.199306 → 0.650033, `poison powder` 0.369957 → 0.650033,
`Jungle` 0.000000 → 0.274025, `hydro pump` 0.779908 → 1.000000, `Evolving Skies`
0.544792 → 0.663175, `Team Rocket` 0.174414 → 0.286060, `evolves from eevee`
0.334246 → 0.401305, `draw more cards` 0.036293 → 0.094823. Three of them were
scoring zero before.

**`char`, in full, because it is the worst of them.** It scored 0.172290 at the
reference and scores **0.000000** now, and the honest description is that this
query has never worked rather than that these weights broke it. The query
returns 279 hits at both points — membership does not move — and the difference
is entirely what fills the top ten.

At 8/4/3 the window held ten cards named *Strength Charm*, *Sacred Charm*,
*Bravery Charm* and *Big Charm*, every one matched on `prefix` and `fuzzy-name`
together and every one judged **1**: defensible, since they do start with the
letters typed, but not what anybody means by `char`. At 8/2/1.5 the window holds
ten cards matched on `text` alone and judged **0** — Quilava, whose attack is
literally named *Char*, and nine more whose attack or ability text carries a
token an edit away from it. The seven cards a searcher plainly means, the
Charizard, Charmeleon and Charmander prints, are matched on `prefix` alone at
both points and sit at ranks **61 to 98** in both. They were never in the window
to lose. What the change did was swap ten near-misses for ten irrelevancies, and
nDCG scores that honestly: 0.172290 to zero.

The mechanism is worth naming because it is not specific to this query. `char`
is a short fragment that is also a real token in card text, so the `text` branch
matches it on hundreds of documents and scores them on BM25 term statistics,
while the cards actually wanted are reachable only through `prefix`. Halving
`prefix` is exactly the change that loses that contest. Three- and four-letter
fragments are the as-you-type case, and the judged set contains one of them,
which is too few to tune against — so this decision buys the other forty-nine
queries at `char`'s expense, knowingly, and the fix for it is a better prefix
strategy rather than a larger prefix boost.

**Cost.** Three, all stated rather than discovered later.

*Short prefixes got worse and nothing here fixes them.* `char` scoring zero is a
real regression in a real behaviour. The judged set has one query of that shape,
so the sweep cannot tune for it, and a boost large enough to rescue it is the
boost that was just measured as costing more than it returns everywhere else.

*The pool is bounded by this grid, not by all possible rankings.* Every card any
of the 27 points ranks in a top ten is judged, which is what makes this matrix
fully measured. It does not make the set complete: a boost outside the grid, a
change to the `text` branch, a fourth branch or a different `k` would reach
outside the pool again. `unrated@10` is the instrument that will say so, it is 0
today, and it is not a promise about tomorrow.

*A measured weight is measured against one corpus, one judgment set and one
date.* These two numbers are better founded than the ones they replace, which
were an argument with no evidence. They are not permanent. A reseed, or a
re-pool, is grounds to run the sweep again — which is a ten-second command, and
that is most of what this exercise bought.

---

## ADR 11 — A shadow analyzer index, measured not adopted

**Decision.** The folded, delimiter-aware `name` analysis chain is built as a
**second index**, `cards_v2`, reindexed from the served `cards` and scored
against it — not as a mapping change to the index that is served. It is
measured, written down here, and **not adopted**: nothing aliases it, the server
compiles `cards` in as its index name, and the chain cannot reach production
until a coordinated reseed carries it.

**Why a second index rather than an in-place change.** Analysis lives in the
mapping, and a mapping cannot be edited in place — changing it means creating an
index and reindexing into it. On this project that reindex is a coordinated
two-project change, for the reason [ADR
8](#adr-8--what-was-deliberately-not-built) already records: the companion load
generator's fixtures assert exact corpus cardinalities, so the served index
cannot be rebuilt on this repository's schedule alone. A shadow index buys the
measurement without the coordination. It also buys the safety argument
structurally rather than by care: `/healthz` counts by the name `cards`, every
frozen cardinality aggregates a `keyword` field a text analyzer cannot reach,
and a second index is invisible to both.

**What is in the chain.** A standard tokenizer, then `lowercase`,
`asciifolding`, a one-group `gx, ex` suffix synonym set, a
`word_delimiter_graph` with `split_on_numerics` and `split_on_case_change` off
and `preserve_original` and `stem_english_possessive` on, and `flatten_graph` to
close the graph. The `lc` normalizer gains `asciifolding` too, so `name.kw`,
`evolves_from.kw`, `artist.kw` and `set_name.kw` fold with the text field.
`name.sayt` and `name.suggest` are untouched, which is why the `prefix` branch
and the suggester do not fold.

**The accent gap closes, and that is the result this index was built for.** A
bare `match` against the `name` field alone:

| Spelling | `cards` | `cards_v2` |
|---|---|---|
| `pokemon` | 0 | 71 |
| `pokémon` | 71 | 71 |

On the served index the two spellings are two different terms and the
unaccented one reaches nothing on that field at all. On the shadow index they
are one term.

**The ranking result: it costs a little.** Both sides run the same 50 judged
queries through the same `search.BuildQuery`, at the weights [ADR
10](#adr-10--measured-branch-weights) adopted, over the same 20,324 documents,
with the same `k`. The only variable is the analysis chain.

| Metric | `cards` | `cards_v2` | Δ |
|---|---|---|---|
| nDCG@10 | 0.645359 | 0.637366 | −0.007994 |
| precision@10 | 0.662000 | 0.656000 | −0.006000 |
| MRR@10 | 0.603333 | 0.593056 | −0.010278 |
| unrated@10 | 0 | 3 | +3 |

Per stratum, on the headline metric, with `unrated@10` beside it because a score
can only be read next to it:

| Stratum | nDCG@10 `cards` | nDCG@10 `cards_v2` | Δ | unrated@10 `cards_v2` |
|---|---|---|---|---|
| exact name | 0.971654 | 0.968021 | −0.003633 | 2 |
| prefix | 0.569717 | 0.569717 | 0.000000 | 0 |
| typo | 0.876376 | 0.877415 | **+0.001039** | 0 |
| attack text | 0.845457 | 0.845457 | 0.000000 | 0 |
| artist | 0.795661 | 0.795661 | 0.000000 | 0 |
| set name | 0.327315 | 0.272812 | **−0.054503** | 1 |
| natural-language intent | 0.195589 | 0.195589 | 0.000000 | 0 |

Three strata never move at all, one gains, two lose and one of those loses
nearly everything the headline gave up. Reporting only the overall −0.007994
would have hidden that `set name` fell by seven times the headline, which is
what the per-stratum breakdown exists to prevent.

**Four of fifty queries moved. Every one of them for a reason unrelated to the
accent.**

| Query | Stratum | nDCG@10 | Δ |
|---|---|---|---|
| `Team Rocket` | set name | 0.286060 → 0.000000 | −0.286060 |
| `Jungle` | set name | 0.274025 → 0.178564 | −0.095460 |
| `professor oak` | exact name | 0.865207 → 0.839778 | −0.025429 |
| `mewtoo` | typo | 0.856201 → 0.863471 | +0.007271 |

**Mechanism one: the possessive stem, which is a recall change scored as a
precision loss.** `stem_english_possessive` makes `Team Rocket's Porygon2` index
`team`, `rocket's`, `rocket`, `porygon2` where the served chain indexes only
`team`, `rocket's`, `porygon2`. A query of `Team Rocket` therefore matches two
terms on those names instead of one, and that card's score rises from 30.316648
to 36.109787 while `Here Comes Team Rocket!` — whose tokens did not change —
rises only from 29.979610 to 30.492981. The whole `Team Rocket's …` family
overtakes it, and the two grade-3 cards that held ranks 2 and 3 leave the
window, taking the query from 0.286060 to zero. The same mechanism costs
`professor oak`: `Professor's Research` gains the bare token `professor` and
climbs from ranks 8–10 to ranks 5–7, pushing three grade-2 `Imposter`/`Impostor
Professor Oak` prints down — the corpus carries the name both ways, and these
three are one `Impostor` (`base1-73`) and two `Imposter` (`base4-102`,
`cel25c-73_A`).

That is worth stating precisely, because it is not obviously a defect. The new
chain is *better* at finding cards named `Team Rocket's …` and `Bill's …`; the
judgments encode the set-name reading of `Team Rocket`, under which those cards
are graded 0. `bill` shows the same thing scoring neutral: three `Bill's
Maintenance` prints enter the window and three cards matched only on card text
leave it, the score is identical to six decimals, and two of the three arrivals
carry no judgment at all. A searcher typing `Team Rocket` might mean either
thing. The measurement says what this judgment set says, and this judgment set
reads it as a set name.

**Mechanism two: BM25 length normalization moves for documents that did not
change.** The chain emits more tokens per name — synonyms, preserved originals,
delimiter parts — so the `name` field's average length rises from **1.440366**
to **1.595109**. BM25 divides by that average, so every name-branch score in the
index shifts even where the document's own token stream is untouched. That is
what put `Perilous Jungle` into the `Jungle` window: `weight(name:jungle)` rises
from 8.929465 to 9.375243 purely through the `tf` term (0.392206 → 0.411785),
and a grade-3 Jungle-set card falls out of tenth place. It is also the whole of
`mewtoo`'s gain, in the other direction: `Mewtwo LV.X` lengthens from two tokens
to four, drops below the one-token `Mewtwo` prints, and a grade-3 card rises
from rank 7 to rank 5. An analysis change is not local to the documents whose
tokens it rewrites.

**The synonym group is unmeasured, not vindicated.** No judged query contains
`ex` or `gx` as a token, so the `gx, ex` group's direct effect — the one that
conflates a `-GX` printing with an `ex` printing — is invisible to this set. Its
only measured contribution is indirect, through the average-length inflation
above. The prediction that it would cost the `exact name` stratum was right
about the stratum and wrong about the cause: `exact name` fell, but on
`professor oak`, through the possessive stem. Anyone who reopens this record
should judge a handful of suffix queries before drawing any conclusion about
that group.

**The accent fix is real and reaches no judged window.** It is proven by the
counts above and by the token streams, and yet the two judged queries whose text
contains `pokemon` score identically on both indexes — `switch my active
pokemon` at 1.000000, already at the ceiling, and `water starter pokemon` at
0.000000 with a byte-identical window of `Water Energy` prints that the token
`water` alone decides. The set has no accented query and no query the fold can
reach. That is a gap in the judgment set, and this record is the place it gets
written down rather than the place it gets closed: the set was **not** amended
to suit the index under test, because a judgment set edited to reward the change
being measured is not evidence.

**`unrated@10` rose from 0 to 3**, on two queries: `bill` (ex14-71, ex6-87, both
`Bill's Maintenance` prints) and `Team Rocket` (sv10-155, `Team Rocket's
Porygon-Z`). Three slots out of five hundred, 0.6% of the evaluated window. It
is small, but it is not nothing, and it is the reason ADR 10's adoption rule
would refuse this index outright: a point carrying unjudged hits has not been
measured against the pool. That rule is why this record says *measured* and not
*adopted*. Those three cards score as irrelevant because nobody graded them, not
because anybody judged them so, and every number above is a little more
pessimistic than the truth by exactly that much.

**Cost.** Four, all of them ongoing.

*Two indexes to keep in step.* `cards_v2` is 10.2mb on the same node, built by
one `_reindex` and refreshed by hand. Nothing rebuilds it when `cards` is
reseeded, so from the next reseed onward it is stale unless somebody reindexes
it again, and a stale shadow index scored against a fresh served one would
report an analysis difference that is really a corpus difference. The harness
fails rather than reports if the two document counts diverge, which is the
cheapest available guard and not a substitute for rebuilding it.

*The chain cannot reach production on this repository's schedule.* It needs the
coordinated reseed of ADR 8. Until then the served index has no folding,
docs/RELEVANCE.md says so plainly, and this record is a measurement of
something the deployment does not do.

*The measurement is bounded by a pool drawn from the other index's windows.*
Every judgment here was pooled from windows the served analysis chain returned,
so a chain that surfaces different cards is scored as though the cards it
surfaced were noise. `unrated@10` is the instrument that says how far that went —
3 of 500 — and the honest reading of a −0.007994 headline measured that way is
"no measured improvement, a small measured cost, and a known bias in the
direction of the cost".

**What this does not decide.** Whether to adopt the chain. It costs a little on
this judgment set, it fixes a gap this judgment set cannot see, and the two
facts do not resolve each other. The next step is judgments that can see it — an
accented query, a suffix query, a possessive name — measured before any alias is
moved. [ADR 9](#adr-9--how-relevance-is-evaluated) fixed the method for exactly
this situation: the first numbers are in, and they say the question is not ready
to be closed.
