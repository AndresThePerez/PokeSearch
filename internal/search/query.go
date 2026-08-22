package search

import "strings"

// BuildQuery produces the complete _search body for a validated Params value.
// Keeping this pure makes the generated ES DSL byte-testable and loggable.
//
// Facets are disjunctive: the text query stays in query, the full filter set
// moves to post_filter (hits stay fully filtered), and each facet aggregation
// is scoped by every active filter except its own, so a selection never
// collapses its own alternatives.
func BuildQuery(p Params) map[string]any {
	if isExactIDLookup(p) {
		return map[string]any{
			"size":  1,
			"query": map[string]any{"term": map[string]any{"id": p.ID}},
		}
	}

	body := map[string]any{
		"track_total_hits": true,
		"size":             p.PageSize,
		"sort":             buildSort(p),
		"aggs":             buildAggs(p),
	}
	if p.Page > 1 {
		body["from"] = (p.Page - 1) * p.PageSize
	}
	if p.Q != "" {
		body["query"] = map[string]any{"bool": buildBool(p)}
		body["highlight"] = buildHighlight(p.Q)
	}
	if filter := buildFilters(p, ""); len(filter) > 0 {
		body["post_filter"] = map[string]any{"bool": map[string]any{"filter": filter}}
	}
	return body
}

// isExactIDLookup identifies the deep-link card fetch. It deliberately stays
// narrow so combining id with filters, pagination, an explicit sort, or a
// non-default page size keeps the full search behavior.
func isExactIDLookup(p Params) bool {
	return p.ID != "" && p.Q == "" && p.Supertype == "" && len(p.Types) == 0 &&
		len(p.Rarity) == 0 && len(p.Series) == 0 && p.SetID == "" &&
		p.HPMin == nil && p.HPMax == nil && p.Sort == "newest" && p.Order == "" &&
		p.Page == 1 && p.PageSize == PageSize
}

// Branch is one relevance clause of the text query, carrying the name ES
// reports it under. The names travel three ways: ES echoes the ones a hit
// matched as matched_queries, /api/explain scores each branch on its own, and
// the grid renders them as badges — so "why did this card rank here" is
// answerable from the response alone.
type Branch struct {
	Name  string         // also the clause's _name
	Query map[string]any // the complete should clause
}

// Branches is the relevance registry, the sibling of Facets: one list, read by
// the query builder and by /api/explain, so the two can never rank a card by
// different clauses. The boosts encode an ordering — an exact name beats a
// prefix beats a fuzzy name beats a body-text hit — not measured weights; the
// fourth branch deliberately carries no boost and takes ES's implicit 1.
func Branches(q string) []Branch {
	exact := map[string]any{
		"value": strings.ToLower(q),
		"boost": 8,
	}
	prefix := map[string]any{
		"query": q,
		"type":  "bool_prefix",
		"fields": []any{
			"name.sayt",
			"name.sayt._2gram",
			"name.sayt._3gram",
		},
		"boost": 4,
	}
	fuzzy := map[string]any{
		"query":     q,
		"fuzziness": "AUTO",
		"boost":     3,
	}
	text := map[string]any{
		"query":     q,
		"type":      "best_fields",
		"fuzziness": "AUTO",
		"fields": []any{
			"attacks.name^2",
			"abilities.name^2",
			"attacks.text",
			"abilities.text",
			"flavor_text",
			"set_name^1.5",
			"artist",
		},
	}
	return []Branch{
		named("exact", exact, map[string]any{"term": map[string]any{"name.kw": exact}}),
		named("prefix", prefix, map[string]any{"multi_match": prefix}),
		named("fuzzy-name", fuzzy, map[string]any{"match": map[string]any{"name": fuzzy}}),
		named("text", text, map[string]any{"multi_match": text}),
	}
}

// named stamps the branch name into the clause's own options map — the same
// map the clause already holds — so Branch.Name and the DSL's _name are one
// value written once and cannot drift apart.
func named(name string, options, clause map[string]any) Branch {
	options["_name"] = name
	return Branch{Name: name, Query: clause}
}

func buildBool(p Params) map[string]any {
	branches := Branches(p.Q)
	should := make([]any, 0, len(branches))
	for _, b := range branches {
		should = append(should, b.Query)
	}
	return map[string]any{"should": should}
}

// highlightFragmentSize bounds a body-text fragment. The UI shows one line
// under a card, so a longer fragment would only be truncated by CSS — and it
// would be truncated at an arbitrary character rather than around the match.
const highlightFragmentSize = 120

// HighlightField is one entry of the third registry (after Facets and
// Branches). Whole marks a field whose value comes back complete: a truncated
// card name is useless, while body text has to be cut down to one line.
type HighlightField struct {
	Name  string
	Whole bool
}

// HighlightFields is the registry. It feeds both the highlighter's field
// settings and the highlight_query's field list, so the two cannot disagree —
// and they must not: require_field_match only highlights a field the
// highlight_query actually names.
var HighlightFields = []HighlightField{
	{Name: "name", Whole: true},
	{Name: "attacks.name", Whole: true},
	{Name: "abilities.name", Whole: true},
	{Name: "attacks.text"},
	{Name: "abilities.text"},
	{Name: "flavor_text"},
}

// buildHighlight produces the server-side <mark> tagging for a text query.
//
// Attached only when q is non-empty (D5: on by default for text queries — it
// answers the question the user actually asked, "why is this card in my
// results?"). Browse has nothing to highlight; the exact-ID fast path is a
// single-document fetch.
//
// THE highlight_query IS LOAD-BEARING, NOT DECORATION. The ranking query uses
// fuzziness AUTO on two branches, and the highlighter has to rewrite a fuzzy
// query into the concrete terms it matched, per document, per field. Measured
// against the pinned corpus at q=charizard: 17ms without highlighting, 409ms
// with it — four times the 100ms SLA on its own. Handing the highlighter a
// non-fuzzy query scoped to exactly the highlighted fields brings it back to
// 20ms. Ranking is untouched: this query only decides what gets marked.
//
// The cost of the trade is precise and small: a query that only matched
// fuzzily ("charizrd") still ranks the card, it just gets no snippet. A
// missing snippet is a far better failure than a 400ms search.
//
// The tags are fixed here rather than left to ES's <em> default so the
// frontend parses one known marker — into real DOM nodes, never innerHTML.
func buildHighlight(q string) map[string]any {
	fields := make(map[string]any, len(HighlightFields))
	names := make([]any, 0, len(HighlightFields))
	for _, f := range HighlightFields {
		if f.Whole {
			fields[f.Name] = map[string]any{"number_of_fragments": 0}
		} else {
			fields[f.Name] = map[string]any{
				"fragment_size":       highlightFragmentSize,
				"number_of_fragments": 1,
			}
		}
		names = append(names, f.Name)
	}
	return map[string]any{
		"pre_tags":  []any{"<mark>"},
		"post_tags": []any{"</mark>"},
		"fields":    fields,
		"highlight_query": map[string]any{"multi_match": map[string]any{
			"query":  q,
			"type":   "best_fields",
			"fields": names,
		}},
	}
}

// FacetDef is one entry of the single facet registry. Three things used to
// repeat this knowledge — the aggregation builder, the response decoder and the
// facet map the handler pre-populates — and the rarity terms.size drift (30
// configured against 38 live rarities) is exactly what happens when they
// disagree. They now all read this list.
type FacetDef struct {
	Name    string // response and aggregation key
	Exclude string // the Params filter this facet must not apply to itself
	Field   string // the ES field to aggregate
	Size    int    // terms size; 0 means the ES default
}

// SetsFacet is the one facet whose labels and release dates come from the
// cached set catalog rather than from the per-request aggregation, so the
// handler special-cases it on the way out.
const SetsFacet = "sets"

// Facets is the registry. Sizes are ceilings, not guesses: each is at or above
// the live cardinality of the pinned corpus (types 11 of 11, rarity 38 of 100,
// series 17 of 20, sets 173 of 200), so no terms aggregation ever drops a
// bucket into sum_other_doc_count. Raising a corpus cardinality past one of
// these silently truncates a facet — internal/acceptance asserts against it.
var Facets = []FacetDef{
	{Name: "supertype", Exclude: "supertype", Field: "supertype", Size: 0},
	{Name: "types", Exclude: "types", Field: "types", Size: 11},
	{Name: "rarity", Exclude: "rarity", Field: "rarity", Size: 100},
	{Name: "set_series", Exclude: "series", Field: "set_series", Size: 20},
	{Name: SetsFacet, Exclude: "set", Field: "set_id", Size: 200},
}

// facetByName resolves a registry entry. Anything that wants to aggregate a
// facet's field looks it up here rather than restating the field name.
func facetByName(name string) (FacetDef, bool) {
	for _, f := range Facets {
		if f.Name == name {
			return f, true
		}
	}
	return FacetDef{}, false
}

// termsOf is the one construction of a facet's terms aggregation body. Size 0
// means "no size key": ES's default is already above the cardinality.
func termsOf(f FacetDef) map[string]any {
	terms := map[string]any{"field": f.Field}
	if f.Size > 0 {
		terms["size"] = f.Size
	}
	return terms
}

func buildAggs(p Params) map[string]any {
	aggs := make(map[string]any, len(Facets))
	for _, f := range Facets {
		aggs[f.Name] = facetAgg(p, f)
	}
	return aggs
}

// facetAgg scopes one facet's terms aggregation with every active filter
// except the facet's own.
func facetAgg(p Params, f FacetDef) map[string]any {
	scope := buildFilters(p, f.Exclude)
	terms := termsOf(f)
	if len(scope) == 0 {
		return map[string]any{"terms": terms}
	}
	return map[string]any{
		"filter": map[string]any{"bool": map[string]any{"filter": scope}},
		"aggs": map[string]any{"items": map[string]any{
			"terms": terms,
		}},
	}
}

// statsFacet ties one Stats-view breakdown to the facet it describes. Key is
// what the aggregation and the /api/stats response call it; Facet is the
// registry entry the field and terms size come from.
type statsFacet struct {
	Key   string
	Facet string
}

// statsFacets are the categorical breakdowns /api/stats reports, in the order
// the Stats view renders them. They are named, not restated: a chart that
// claims to show the corpus's rarities must aggregate the same field, at the
// same terms size, as the rarity filter beside it. The sets facet is left out
// deliberately — 173 bars is a list, not a chart.
var statsFacets = []statsFacet{
	{Key: "types", Facet: "types"},
	{Key: "supertype", Facet: "supertype"},
	{Key: "rarity", Facet: "rarity"},
	{Key: "series", Facet: "set_series"},
}

// hpBucketWidth is the width of one HP band. 30 puts the corpus's 0–380 HP
// range into thirteen bars — enough resolution to see power creep move, few
// enough that the chart stays readable on a phone.
const hpBucketWidth = 30

// BuildStatsQuery aggregates the immutable corpus for /api/stats: prints per
// year, the HP distribution, the facet breakdowns and the maximum HP, in one
// request that carries no hits (size 0).
//
// It takes no Params on purpose. The Stats view describes the whole archive,
// never the current search, so the body is a constant — which is what lets the
// server compute it once and cache it for the process lifetime. The index only
// changes via reseed, and reseed already implies a restart.
//
// min_doc_count 0 on both histograms keeps empty buckets: a year nobody
// printed a card in, and an HP band nobody occupies, are the interesting parts
// of the shape and dropping them would silently rescale the axis.
func BuildStatsQuery() map[string]any {
	aggs := map[string]any{
		"per_year": map[string]any{"date_histogram": map[string]any{
			"field":             "release_date",
			"calendar_interval": "year",
			"min_doc_count":     0,
		}},
		"hp": map[string]any{"histogram": map[string]any{
			"field":         "hp",
			"interval":      hpBucketWidth,
			"min_doc_count": 0,
		}},
		"max_hp": map[string]any{"max": map[string]any{"field": "hp"}},
	}
	for _, sf := range statsFacets {
		def, ok := facetByName(sf.Facet)
		if !ok {
			continue // unreachable: TestStatsBreakdownsFollowFacetRegistry guards it
		}
		aggs[sf.Key] = map[string]any{"terms": termsOf(def)}
	}
	return map[string]any{
		"track_total_hits": true,
		"size":             0,
		"aggs":             aggs,
	}
}

// BuildSetCatalogQuery produces the cacheable, match-all catalog request used
// to attach stable set labels and release dates to dynamic per-search counts.
func BuildSetCatalogQuery() map[string]any {
	return map[string]any{
		"track_total_hits": false,
		"size":             0,
		"aggs": map[string]any{"set_catalog": map[string]any{
			"terms": map[string]any{"field": "set_id", "size": 200},
			"aggs": map[string]any{"identity": map[string]any{"top_hits": map[string]any{
				"size":    1,
				"_source": map[string]any{"includes": []string{"set_name", "release_date"}},
			}}},
		}},
	}
}

// buildFilters returns every active filter except the one named by exclude
// ("" excludes nothing). ID and HP have no facet, so they are always kept.
func buildFilters(p Params, exclude string) []any {
	var filter []any
	if p.ID != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"id": p.ID}})
	}
	if p.Supertype != "" && exclude != "supertype" {
		filter = append(filter, map[string]any{"term": map[string]any{"supertype": p.Supertype}})
	}
	if len(p.Types) > 0 && exclude != "types" {
		filter = append(filter, map[string]any{"terms": map[string]any{"types": p.Types}})
	}
	if len(p.Rarity) > 0 && exclude != "rarity" {
		filter = append(filter, map[string]any{"terms": map[string]any{"rarity": p.Rarity}})
	}
	if len(p.Series) > 0 && exclude != "series" {
		filter = append(filter, map[string]any{"terms": map[string]any{"set_series": p.Series}})
	}
	if p.SetID != "" && exclude != "set" {
		filter = append(filter, map[string]any{"term": map[string]any{"set_id": p.SetID}})
	}
	if p.HPMin != nil || p.HPMax != nil {
		r := map[string]any{}
		if p.HPMin != nil {
			r["gte"] = *p.HPMin
		}
		if p.HPMax != nil {
			r["lte"] = *p.HPMax
		}
		filter = append(filter, map[string]any{"range": map[string]any{"hp": r}})
	}
	return filter
}

// buildSort always adds an id tiebreaker so pagination remains deterministic.
func buildSort(p Params) []any {
	tiebreaker := map[string]any{"id": "asc"}
	switch p.Sort {
	case "newest":
		return []any{map[string]any{"release_date": "desc"}, tiebreaker}
	case "oldest":
		return []any{map[string]any{"release_date": "asc"}, tiebreaker}
	case "hp":
		return []any{map[string]any{"hp": map[string]any{
			"missing": "_last",
			"order":   orderOr(p.Order, "desc"),
		}}, tiebreaker}
	case "name":
		return []any{map[string]any{"name.kw": orderOr(p.Order, "asc")}, tiebreaker}
	default:
		return []any{"_score", tiebreaker}
	}
}

func orderOr(order, fallback string) string {
	if order == "asc" || order == "desc" {
		return order
	}
	return fallback
}

// BuildDidYouMean asks the term suggester for per-token corrections of q.
//
// D8: this runs only on a zero-result text search, which is both the cheapest
// response shape there is and the only moment a correction cannot compete with
// the autocomplete the user was already being offered.
//
// size:0 because the caller wants the suggestions and nothing else. Without
// it ES answers an implicit match_all with ten fetched documents that are
// thrown away — on the one request shape that exists because nothing matched.
func BuildDidYouMean(q string) map[string]any {
	return map[string]any{
		"size": 0,
		"suggest": map[string]any{"dym": map[string]any{
			"text": q,
			"term": map[string]any{
				"field": "name",
				// popular: only offer a correction that is more common in the
				// index than what was typed, or a rare misspelling "corrects"
				// to an equally rare one and helps nobody.
				"suggest_mode": "popular",
				"size":         1,
			},
		}},
	}
}

// SuggestSize is how many completions /api/suggest asks ES for. The frontend
// slices to the same number, so the two must not drift.
const SuggestSize = 8

// BuildSuggest produces the completion-suggester body for /api/suggest.
func BuildSuggest(q string, fuzzy bool) map[string]any {
	completion := map[string]any{
		"field":           "name.suggest",
		"size":            SuggestSize,
		"skip_duplicates": true,
	}
	if fuzzy {
		completion["fuzzy"] = map[string]any{"fuzziness": "AUTO"}
	}
	return map[string]any{"suggest": map[string]any{"card": map[string]any{
		"prefix":     q,
		"completion": completion,
	}}}
}
