# Relevance

Every other test in this repository asserts that the query is *stable*. This one
asserts that the ranking is *good*. It replays a fixed set of judged queries
through Elasticsearch's `_rank_eval` API and reports nDCG@10, precision@10 and
MRR@10, overall and per stratum.

The method is [ADR 9](DECISIONS.md#adr-9--how-relevance-is-evaluated). The
weights the numbers below describe are
[ADR 10](DECISIONS.md#adr-10--measured-branch-weights). The harness is
`internal/relevance`, tag-gated behind `relevance` because it needs a seeded
cluster and a plain `go test ./...` must not.

Every figure on this page is read out of `docs/relevance/baseline.json` or out
of the sweep that ADR 10 records. None of them is an estimate.

## The headline

| Metric | Value |
|---|---|
| **nDCG@10** | **0.645359** |
| precision@10 | 0.662000 |
| MRR@10 | 0.603333 |
| unrated@10 | 0 |

Measured on index `cards` at **20,324** documents, over **50** judged queries,
at **k = 10**. The build identity recorded beside it is `dev` / `none` /
`unknown`, and the corpus `seed` is `null` — the index predates provenance
stamping, which is reported rather than repaired.

nDCG@10 is the headline because it is the only one of the three that reads both
halves of what a judgment says: the *grade* of a hit and *where the hit landed*.
precision@10 stays because it is the one number a reader interprets correctly at
a glance — it counts hits in the window and can see neither grade nor order.
MRR@10 stays for the strata where there is a single right answer and its rank is
the whole story; it counts only grade-3 hits, because a defensible near-miss at
rank 1 is not the right answer.

`unrated@10` is not a quality metric. It is the instrument described under
[the judgment set](#the-judgment-set) below.

## By stratum

The seven strata are ADR 9's seven named failure modes. They exist so that a
metric which moves can be attributed to one of them instead of averaged into
invisibility — a typo fix that quietly costs artist search is exactly the trade
this table is meant to expose.

| Stratum | Queries | nDCG@10 | precision@10 | MRR@10 | unrated@10 |
|---|---|---|---|---|---|
| exact name | 7 | 0.971654 | 0.885714 | 1.000000 | 0 |
| prefix | 7 | 0.569717 | 0.614286 | 0.511905 | 0 |
| typo | 7 | 0.876376 | 0.985714 | 0.875000 | 0 |
| attack text | 7 | 0.845457 | 0.785714 | 0.750000 | 0 |
| artist | 7 | 0.795661 | 0.814286 | 0.690476 | 0 |
| set name | 7 | 0.327315 | 0.314286 | 0.339286 | 0 |
| natural-language intent | 8 | 0.195589 | 0.287500 | 0.125000 | 0 |
| **overall** | **50** | **0.645359** | **0.662000** | **0.603333** | **0** |

What the table says, plainly. Typing a name works: exact name reaches MRR@10
1.000000, meaning the card the searcher plainly meant is at rank 1 for all seven
queries, and typo is close behind at precision@10 0.985714. The floor is at the
other end. Natural-language intent scores nDCG@10 0.195589 and set name
0.327315 — a search engine built out of name and text branches answers "what is
this card called" well and "what does this card do, roughly, in my own words"
badly, and no boost weight fixes that. Prefix sits in the middle at 0.569717 and
is dragged there by short fragments; see [`char`](#char-in-full) below.

These are the strata a future change should be read against, not the overall
number. The overall number is an average of seven different questions.

## The judgment set

`internal/relevance/testdata/judgments.json` holds **50 queries** carrying **769
judgments** over **636 distinct card ids**, spread over the seven strata: seven
queries each except natural-language intent, which has eight.

**The grades.** Each judged card is graded 0 to 3 — irrelevant, then two grades
of defensible, then the card the searcher plainly meant. Graded rather than
binary because relevance here is not binary: a different print of the right card
is not the answer, but it is not noise either.

**How they were made.** The grading is rule-based: one grading rule decided
every card judged for a given query, and each judgment records a one-line reason
for the grade it earned under that rule. The rule itself is not a field in the
file — `judgments.json` carries `query`, `stratum` and `judgments: [{id, grade,
reason}]`, and nothing else. What holds a query's rule is the reasons that apply
it, plus the maintainer approval recorded below. **The reasons are what ships** —
all 769 of them, beside the grades — and they are what makes the set auditable: a
disagreement later is a conversation about a stated argument rather than an
argument about a number.

**Every judged id is checked against the corpus before any number is computed.**
`_rank_eval` answers a rating naming a document the index does not hold with an
HTTP 200 and no failure entry: the rating is silently dropped and the query's
score falls as though the ranking had missed a card. A typo in the judgment file
would therefore read as a relevance regression. The harness rules that out with
one `_mget` rather than trusting the file, and every one of the 636 ids is
present.

**The pool was drawn from the whole sweep, not from one ranking.** A judgment
set can only reward documents somebody judged, so it measures *movement away
from a known-good window* well and *discovery* badly. The first version of this
set was pooled from the windows the weights of the day returned, which made it
biased in favour of those weights: the sweep behind ADR 10 then surfaced 69
hits out of a 500-slot window — 13.8% of it — that nobody had graded. The set
was re-pooled rather than read around. **117 judgments were added, taking it
from 652 to 769, so that every card any of the sweep's 27 grid points ranks in a
top ten is graded.** `unrated@10` is **0** at all 27 points and in all seven
strata, which is what makes the adopted weights measured rather than merely
better-scoring: no window the decision rests on contains a card nobody looked
at.

**Review.** The re-pooled set was reviewed and approved by the maintainer on
**2026-09-13**, and that approval is what these numbers rest on.

**What `unrated@10` is for.** It counts hits inside the evaluated windows that
no judgment covers, and it is reported beside the three scores so that a score
which fell can be read correctly. If `unrated@10` held steady, the ranking put
judged cards in a worse order. If it rose, the ranking reached outside the pool
and the set needs re-pooling before the number means anything. It is 0 today,
and that is a measurement, not a promise about tomorrow: a boost outside the
grid, a change to the `text` branch, a fourth branch or a different `k` would
reach outside the pool again.

## What the measured weights changed

ADR 10 swept 27 boost points — `exact` over 4 / 8 / 16, `prefix` over 2 / 4 / 8,
`fuzzy-name` over 1.5 / 3 / 6, with `text` held at its implicit 1 — and adopted
**8 / 2 / 1.5**. The reference point below is **8 / 4 / 3**, the weights served
before that work; every delta is measured against it.

| | Point | nDCG@10 | precision@10 | MRR@10 | unrated@10 |
|---|---|---|---|---|---|
| Reference | 8 / 4 / 3 | 0.585617 | 0.642000 | 0.547167 | 0 |
| **Adopted** | **8 / 2 / 1.5** | **0.645359** | **0.662000** | **0.603333** | **0** |
| Runner-up | 8 / 4 / 1.5 | 0.641568 | 0.676000 | 0.585556 | 0 |

**+0.059743 nDCG@10, +0.020000 precision@10, +0.056167 MRR@10.** The runner-up
loses the headline by only **0.003791** and scores a higher precision@10, so it
is the place to start if this decision is reopened. The grid's best precision@10
is neither of them: **0.682000**, at `prefix` 8 with `fuzzy-name` 1.5.

### By stratum, reference to adopted

| Stratum | 8/4/3 | 8/2/1.5 | Δ nDCG@10 |
|---|---|---|---|
| exact name | 0.992428 | 0.971654 | −0.020774 |
| prefix | 0.546714 | 0.569717 | +0.023003 |
| typo | 0.908825 | 0.876376 | −0.032449 |
| attack text | 0.478453 | 0.845457 | **+0.367004** |
| artist | 0.795661 | 0.795661 | 0.000000 |
| set name | 0.255307 | 0.327315 | +0.072008 |
| natural-language intent | 0.179890 | 0.195589 | +0.015699 |

Five of seven strata improve, artist does not move at all, and exact name and
typo each give up a little. The whole gain is one trade: the name branches were
loud enough to drown the card-text branch, and quieting them let card text be
heard.

### The queries that moved

Six of fifty got worse, eleven got better, thirty-three did not move.

| Δ nDCG@10 | Query | Stratum | Before → after |
|---|---|---|---|
| −0.219868 | `pikchu` | typo | 0.498300 → 0.278432 |
| −0.172290 | `char` | prefix | 0.172290 → 0.000000 |
| −0.135685 | `dragoni` | prefix | 0.641046 → 0.505360 |
| −0.134793 | `professor oak` | exact name | 1.000000 → 0.865207 |
| −0.010623 | `bill` | exact name | 0.946994 → 0.936371 |
| −0.007271 | `mewtoo` | typo | 0.863471 → 0.856201 |
| +0.058531 | `draw more cards` | natural-language intent | 0.036293 → 0.094823 |
| +0.067059 | `evolves from eevee` | natural-language intent | 0.334246 → 0.401305 |
| +0.111646 | `Team Rocket` | set name | 0.174414 → 0.286060 |
| +0.118383 | `Evolving Skies` | set name | 0.544792 → 0.663175 |
| +0.220092 | `hydro pump` | attack text | 0.779908 → 1.000000 |
| +0.274025 | `Jungle` | set name | 0.000000 → 0.274025 |
| +0.280076 | `poison powder` | attack text | 0.369957 → 0.650033 |
| +0.450727 | `thunder jolt` | attack text | 0.199306 → 0.650033 |
| +0.469000 | `pika` | prefix | 0.531000 → 1.000000 |
| +0.618131 | `rain dance` | attack text | 0.000000 → 0.618131 |
| +1.000000 | `energy burn` | attack text | 0.000000 → 1.000000 |

Three of the eleven were scoring zero before.

### `char`, in full

It is the loss worth reading, because it is the one that names a real gap rather
than a trade. `char` scored 0.172290 at the reference and scores **0.000000**
now, and the honest description is that this query has never worked rather than
that these weights broke it.

The query returns **279** hits at both points — membership does not move — and
the difference is entirely what fills the top ten. At 8/4/3 the window held ten
cards named *Strength Charm*, *Sacred Charm*, *Bravery Charm* and *Big Charm*,
matched on `prefix` and `fuzzy-name` together and every one judged **1**:
defensible, since they do start with the letters typed, but not what anybody
means by `char`. At 8/2/1.5 the window holds ten cards matched on `text` alone
and judged **0** — Quilava, whose attack is literally named *Char*, and nine
more whose attack or ability text carries a token an edit away from it. The
seven cards a searcher plainly means — the Charizard, Charmeleon and Charmander
prints — are matched on `prefix` **alone** and sit at ranks **61 to 98 at both
weightings**. They were never in the window to lose. The change swapped ten
near-misses for ten irrelevancies, and nDCG scores that honestly.

The mechanism is not specific to this query. A short fragment that is also a
real token in card text matches `text` across hundreds of documents and is
scored there on BM25 term statistics, while the cards actually wanted are
reachable only through `prefix`. Halving `prefix` loses that contest. The judged
set holds one query of that shape, which is too few to tune against, so the
adopted point buys the other forty-nine queries at `char`'s expense —
knowingly, and the fix is a better prefix strategy rather than a larger prefix
boost.

## The gate

The `acceptance` workflow runs this harness after the contract matrix, so a
broken contract fails before a ranking metric is even computed. It writes the
fresh run to a scratch path, never over the committed baseline, and then
compares the two. The job goes red on **either** of:

| Threshold | Value | What it protects against |
|---|---|---|
| `NDCG_TOLERANCE` | 0.01 | A ranking regression. A re-run against an unchanged index rewrites the baseline byte for byte, so the expected delta is exactly zero; the budget exists for re-seed jitter. At a sixth of the +0.059743 the adopted weights bought, no change undoing them fits inside it. |
| `MAX_UNRATED` | 0 | A boost change that reached outside the judged pool. Hits no judgment covers score as irrelevant because nobody looked at them, so a run carrying any has not been measured against the pool at all and its score is worth nothing until the set is re-pooled. |

Only `TestRelevanceBaseline` runs there. The boost sweep is an offline tool that
fails by design when its best grid point is not fully judged — a request to
judge more cards, not a regression — and it must not paint that job red for the
wrong reason.

The gate lives in `acceptance` and not in `ci.yml` on purpose: an evaluation
needs a seeded cluster, and the fast job set deliberately has none. That is why
it is on demand rather than on every push.

## Reproducing the run

Against a seeded cluster and a running app:

```bash
POKESEARCH_ES=http://localhost:9200 \
POKESEARCH_URL=http://localhost:8080 \
  go test -tags relevance -count=1 -run TestRelevanceBaseline -v ./internal/relevance
```

Four environment variables, no flags and no config file:

| Variable | Default | What it does |
|---|---|---|
| `POKESEARCH_ES` | `http://localhost:9200` | Elasticsearch base URL the `_rank_eval` requests go to. |
| `POKESEARCH_URL` | `http://localhost:8080` | PokéSearch base URL, read for the `/api/meta` build identity stamped into the baseline. |
| `POKESEARCH_INDEX` | `cards` | The index the requests target and the ratings name. Point it at a shadow index to evaluate one. |
| `POKESEARCH_BASELINE_OUT` | `docs/relevance/baseline.json` | Where the run is written. Point it elsewhere for a comparison run. |

The baseline carries no timestamp, so a re-run against an unchanged index
rewrites the same bytes. That is what makes a diff of it mean *the ranking
moved* and nothing else. To compare without touching the committed file:

```bash
POKESEARCH_BASELINE_OUT=/tmp/relevance-run.json \
  go test -tags relevance -count=1 -run TestRelevanceBaseline ./internal/relevance
diff docs/relevance/baseline.json /tmp/relevance-run.json
```

The full 27-point sweep behind ADR 10 is a separate, offline test in the same
package:

```bash
go test -tags relevance -count=1 -run TestBoostSweep -v ./internal/relevance
```

## Design notes moved from the README

These paragraphs were the README's `### Relevance design` prose. They moved here
whole when the README was shortened; only the relative link prefix to
`DECISIONS.md` changed, because this file lives in `docs/`.

Those weight points stay inspectable from the running application: `GET /api/compare?q=…` runs the same query twice under two of them — `served`, `previous` or `runner-up`, defaulting to `served` against `previous` — and reports both top-ten windows, the per-card movement between them and Spearman's ρ over their union. The rail's **Ranking lab** panel is its only client: open it on a result page and it shows the two rankings side by side with the cards that moved. The weights are never read off the query string, so the comparison is always between two recorded profiles rather than an arbitrary one.

The four branch names are a contract, not a comment. Elasticsearch echoes the ones each hit matched (`matched_queries`), so a text search's response carries a `matched` array aligned index-for-index with `results` — the grid renders them as per-card badges, and the boost hierarchy becomes observable: sort by relevance and watch the badge mix shift down the page. `GET /api/explain?id=…&q=…` takes the same question one card deeper, replaying each branch against that single document through Elasticsearch's `_explain` and reporting what each contributed. A `should` query's clauses sum, so the matched branches add back up to the score the card was ranked by — which is what the modal's score bars draw. It is deliberately on demand and single-document: Lucene explain on 24 hits per keystroke is pure waste. One case is worth pre-empting, because it can read as a broken badge strip: on an exact-name query the mix does not shift at the top of the list at all — `q=charizard` returns `["exact","prefix","fuzzy-name","text"]` for each of the first three hits, because a name typed exactly satisfies all four branches at once. What separates those hits there is not which badges they carry but how much each branch contributed — the per-branch **scores**, one click away in the explain modal.

A text query also carries `highlights` (aligned the same way) and, when it found nothing, `did_you_mean`. Highlighting is on by default because it answers the question the reader actually asked — *why is this card in my results?* — and the fragments are `<mark>`-tagged server-side over card names, attack and ability names and text, and flavor text. The highlighter runs against its own non-fuzzy `highlight_query`: rewriting the ranking query's `fuzziness: AUTO` per document and per field costs 409 ms on this corpus against 20 ms for the scoped query, so ranking and marking are deliberately two different questions. The cost of that trade is exact and small — a card matched only by a typo still ranks, it just gets no snippet. `did_you_mean` comes from a term suggester that runs only on a zero-result text search: the cheapest response shape there is, and the one moment a correction cannot compete with the autocomplete the reader was already being offered. That gate is narrower than it reads, because the suggester competes with the edit budget the `fuzzy-name` and `text` branches already spend as `fuzziness: AUTO`: a typo those branches can still reach returns hits, which suppresses the suggestion, and a query mangled past every budget returns neither hits nor a correction — `q=charizzzard` answers **107** hits with no `did_you_mean`, and `q=zzzzqqqq` answers **0** hits, also with no `did_you_mean`. A correction fires in the gap between the two budgets, and the gap exists because they are shaped differently: `fuzziness: AUTO` scales with term length — no edits below three characters, one at three to five, two at six and up — while the term suggester sets no `max_edits` and so takes Elasticsearch's flat default of two at any length. So `q=eevio` — five characters, two edits from `Eevee` — is a single token the branches cannot reach and the suggester can: **0** hits carrying `did_you_mean: eevee`. Token count is not what decides it: `q=pikchu zzzzqqqq` puts one badly mangled token beside one the branches can still reach, and that reachable token alone brings back **267** hits, so the zero-result gate never opens and no correction is offered.

Filters (`supertype`, `types`, `rarity`, `set_series`, `set_id`, HP range) are scoring-neutral. With `q` empty there is nothing meaningful to score, so browse mode sorts by release date instead — match-all scores are noise, and asking for `sort=relevance` without a query is silently downgraded to `newest` rather than rejected. Every sort appends ascending card ID as a deterministic tiebreaker, which is what keeps page boundaries stable.

Facets are **disjunctive**: each one is computed with every active filter *except its own*, so selecting `Rare` does not collapse the rarity dropdown to `Rare`, and the count next to an option predicts what clicking it will do. See [ADR 3](DECISIONS.md#adr-3--disjunctive-facets-via-post_filter).

#### Scoring model and analysis

Underneath those boosts the ranking is **stock BM25** — Elasticsearch's default similarity with default `k1` and `b`, and no `similarity` override anywhere in the mapping. That is a decision, not an omission. What decides a match here is short name fields over a small corpus that is immutable between reseeds, which is the shape BM25's defaults already fit, and nothing has been measured that would justify moving term saturation or length normalization off them. Retuning a similarity with no judgement set to score the change against is guessing with extra steps.

The analysis chain is deliberately just as plain. Every `text` field — `name`, `evolves_from`, `artist`, `set_name`, attack and ability names and text, and `flavor_text` — uses the **standard analyzer**; the index defines no custom analyzer at all. The one thing the settings do add is a lowercase normalizer, `lc`, applied to exactly four keyword sub-fields: `name.kw`, `evolves_from.kw`, `artist.kw` and `set_name.kw`. Those four are the ones a human types, so case must not decide the match. The bare keyword fields behind the filter rail — `supertype`, `subtypes`, `types`, `rarity`, `set_id`, `set_series` — get no normalizer on purpose: their values are a closed vocabulary the facets hand back to the client, which then sends them back verbatim, so folding case there would buy nothing and could merge two values the aggregation counts apart.

`name` carries three sub-fields, and each is a different Elasticsearch feature rather than a second copy of the same text. `name.kw` is the lowercase-normalized keyword the `exact` branch runs its `term` against. `name.sayt` is a **`search_as_you_type`** field, and the `_2gram` and `_3gram` sub-fields it generates are what the `prefix` branch's `bool_prefix` actually matches. `name.suggest` is a **completion suggester**, which Elasticsearch holds in memory as an FST. It is now `/api/suggest`'s fallback rather than its main path: the endpoint first asks a `terms` aggregation over `name.kw` to rank completions by print count, and drops to the suggester — and then to a fuzzy retry — only for a prefix the aggregation cannot serve. The API table above says what that endpoint does; this is the feature vocabulary behind it.

What the chain does *not* have is `asciifolding`, and the corpus's own name is where that shows: `q=pokemon` returns **13,386** hits while `q=pokémon` returns **13,564** — a gap of **178** documents on the word the archive is named after. The unaccented spelling is not lost, but it is carried by the wrong branch. With nothing folding `é` to `e`, no indexed token starts with `pokemon`, so the ASCII query cannot match `prefix` at all. The **71** cards carrying *Pokémon* in their name are then reachable only through `fuzzy-name`, and at that branch's measured boost of **1.5** they rank below every card the `text` branch matched — the whole first page is `text`-only and the first of the 71 lands at rank **105**. The accented spelling matches `prefix` and `fuzzy-name` together and puts **46** of those same cards inside the first hundred. Closing that means adding `asciifolding` to the analysis chain, and analysis lives in the mapping, so it needs a reindex — which on this project is the coordinated two-project change described in [ADR 8](DECISIONS.md#adr-8--what-was-deliberately-not-built), because the [companion load generator](https://github.com/AndresThePerez/Courier)'s fixtures assert exact corpus cardinalities. So folding waits for that reindex, and the divergence is written down here with its numbers rather than left for a reader to trip over.

That reindex now has a **measured** answer waiting for it. `cards_v2` is a shadow index — the same 20,324 documents, built by `_reindex` from `cards` — carrying a real analysis chain on `name`: a standard tokenizer, then `lowercase`, `asciifolding`, a small `gx, ex` suffix synonym group, a `word_delimiter_graph` and `flatten_graph`; the `lc` normalizer folds there too, so `name.kw` and the three other normalized keywords fold with it. **It is not what the live deployment serves.** Nothing aliases it, the server compiles `cards` in as its index name, and `/healthz`, every facet cardinality and every acceptance fixture still describe `cards` exactly as they did. The served chain has no folding; this paragraph is about a second index that does, and it exists so the paragraph above can be measured instead of asserted.

What the chain changes, in tokens — one hyphenated name and one accented one, each read back from that index's own `name` field with `_analyze`:

| Name | `cards` (served) | `cards_v2` (shadow) |
|---|---|---|
| `Ethan's Ho-Oh ex` | `["ethan's","ho","oh","ex"]` | `["ethan's","ethan","ho","oh","ex","gx"]` |
| `Pokémon Center Lady` | `["pokémon","center","lady"]` | `["pokemon","center","lady"]` |

The hyphen was never the problem: the standard tokenizer already split `Ho-Oh`. What the chain adds is the possessive stem (`ethan's` keeps its original token and gains `ethan`), the suffix synonym (`ex` and `gx` reach one another), and the fold. The fold is the headline, and it is a count rather than an argument — a bare, **non-fuzzy** `match` against the `name` field alone, no other branch involved, returns **0** documents for `pokemon` on `cards` and **71** on `cards_v2`, while `pokémon` returns **71** on both. On the served index the two spellings are two different terms; on the shadow index they are one term, and `name.kw` folds with them (`["pokémon center lady"]` becomes `["pokemon center lady"]`).

Scored against the judged set of [ADR 9](DECISIONS.md#adr-9--how-relevance-is-evaluated), at the weights of [ADR 10](DECISIONS.md#adr-10--measured-branch-weights), with the same `BuildQuery` and the same window on both sides, the shadow chain is **slightly worse overall**: nDCG@10 **0.645359 → 0.637366**. Four of the fifty judged queries moved, and none of them moved because of the accent. [ADR 11](DECISIONS.md#adr-11--a-shadow-analyzer-index-measured-not-adopted) carries the per-stratum table and names the two mechanisms; both runs are checked in side by side as `docs/relevance/baseline.json` and `docs/relevance/baseline-v2.json`.
