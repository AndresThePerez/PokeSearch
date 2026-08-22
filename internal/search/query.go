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
	terms := map[string]any{"field": f.Field}
	if f.Size > 0 {
		terms["size"] = f.Size
	}
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
