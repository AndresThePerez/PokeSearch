// Package relevance scores the ranking against a judged relevance set.
//
// It is the offline half of ADR 9. Every other test in this repository asserts
// that the query is *stable*; this one asserts that the ranking is *good*, by
// replaying a fixed set of judged queries through Elasticsearch's _rank_eval
// API and reporting nDCG@10, precision@10 and MRR@10 per stratum and overall.
//
// It is tag-gated, like internal/acceptance and for the same reason — it needs
// a seeded cluster, and a plain `go test ./...` must not:
//
//	go test -tags relevance ./internal/relevance -v
//
// # The judgments
//
// testdata/judgments.json holds 50 queries spread over the seven strata ADR 9
// names — exact name, prefix, typo, attack text, artist, set name and
// natural-language intent — each carrying a list of judged card ids graded 0
// (irrelevant) to 3 (the card the searcher plainly meant), and each grade
// carrying a one-line reason. The reasons are the point: they make the set
// auditable, so a disagreement later is a conversation about a stated argument
// rather than about a number somebody typed.
//
// Each query's judgments cover the whole ten-document window the ranking
// returned when the set was written, plus, where the ranking missed them,
// documents that are plainly the intended answer. Judging the full window is
// what makes precision@10 readable at a glance: an unjudged document counts as
// irrelevant, so a half-judged window would report a precision that no reader
// would interpret correctly.
//
// The known bias is the other side of that coin. A fixed judgment set can only
// reward documents somebody judged, so a ranking change that surfaces a
// genuinely good but unjudged card is scored as if it had surfaced noise. The
// set therefore measures *movement away from a known-good window* well and
// *discovery* badly, and it has to be re-pooled whenever the boosts change
// enough to reshape the windows it was drawn from.
//
// # The query it evaluates
//
// The harness never restates the DSL. It parses each judged query through
// search.ParseParams and builds the body with search.BuildQuery, so the
// evaluated query cannot drift from the served one; internal/search stays
// pure, and nothing here is pushed back into it. Two keys are dropped from
// that body before it is sent — aggregations and highlighting — because
// _rank_eval rejects a request carrying either, and neither influences the
// order of the hits. A test fails loudly if BuildQuery ever grows a third
// top-level key, rather than letting the evaluated query quietly diverge.
//
// # Configuration
//
// Four environment variables, no flags and no config file:
//
//	POKESEARCH_ES           Elasticsearch base URL. Default http://localhost:9200.
//	POKESEARCH_URL          Pokesearch base URL, read for /api/meta build
//	                        identity. Default http://localhost:8080.
//	POKESEARCH_INDEX        Index the _rank_eval requests target, and the index
//	                        the ratings name. Default "cards"; a shadow index
//	                        is evaluated by pointing this at it.
//	POKESEARCH_BASELINE_OUT Where the run is written. Default
//	                        docs/relevance/baseline.json under the module root,
//	                        which is resolved by walking up from the package
//	                        directory (go test runs with that as its working
//	                        directory) until go.mod is found. A comparison run
//	                        points this somewhere else and diffs the two.
//
// The baseline names the index, its document count and the build identity it
// was measured on, so the number can never be read as describing something
// else. It carries no timestamp: a re-run against an unchanged index rewrites
// the same bytes, which is what makes a diff of it mean "the ranking moved".
package relevance
