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
	body := map[string]any{
		"track_total_hits": true,
		"from":             (p.Page - 1) * PageSize,
		"size":             PageSize,
		"query":            map[string]any{"bool": buildBool(p)},
		"sort":             buildSort(p),
		"aggs":             buildAggs(p),
	}
	if filter := buildFilters(p, ""); len(filter) > 0 {
		body["post_filter"] = map[string]any{"bool": map[string]any{"filter": filter}}
	}
	return body
}

func buildBool(p Params) map[string]any {
	b := map[string]any{}
	if p.Q == "" {
		b["must"] = []any{map[string]any{"match_all": map[string]any{}}}
		return b
	}
	b["should"] = []any{
		map[string]any{"term": map[string]any{"name.kw": map[string]any{
			"value": strings.ToLower(p.Q),
			"boost": 8,
		}}},
		map[string]any{"multi_match": map[string]any{
			"query": p.Q,
			"type":  "bool_prefix",
			"fields": []any{
				"name.sayt",
				"name.sayt._2gram",
				"name.sayt._3gram",
			},
			"boost": 4,
		}},
		map[string]any{"match": map[string]any{"name": map[string]any{
			"query":     p.Q,
			"fuzziness": "AUTO",
			"boost":     3,
		}}},
		map[string]any{"multi_match": map[string]any{
			"query":     p.Q,
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
		}},
	}
	b["minimum_should_match"] = 1
	return b
}

func buildAggs(p Params) map[string]any {
	return map[string]any{
		"supertype":  facetAgg(p, "supertype", "supertype", 3),
		"types":      facetAgg(p, "types", "types", 11),
		"rarity":     facetAgg(p, "rarity", "rarity", 100),
		"set_series": facetAgg(p, "series", "set_series", 20),
		"sets":       facetAgg(p, "set", "set_id", 200),
		// The catalog stays global on purpose: it owns the stable 173 set
		// labels; per-request counts come from the scoped sets agg above.
		"set_catalog": map[string]any{
			"global": map[string]any{},
			"aggs": map[string]any{"items": map[string]any{
				"terms": map[string]any{"field": "set_id", "size": 200},
				"aggs": map[string]any{"identity": map[string]any{"top_hits": map[string]any{
					"size":    1,
					"_source": map[string]any{"includes": []string{"set_name", "release_date"}},
				}}},
			}},
		},
	}
}

// facetAgg scopes one facet's terms aggregation with every active filter
// except the facet's own (exclude = the Params filter to leave out).
func facetAgg(p Params, exclude, field string, size int) map[string]any {
	scope := buildFilters(p, exclude)
	if scope == nil {
		scope = []any{}
	}
	return map[string]any{
		"filter": map[string]any{"bool": map[string]any{"filter": scope}},
		"aggs": map[string]any{"items": map[string]any{
			"terms": map[string]any{"field": field, "size": size},
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

// BuildSuggest produces the completion-suggester body for /api/suggest.
func BuildSuggest(q string, fuzzy bool) map[string]any {
	completion := map[string]any{
		"field":           "name.suggest",
		"size":            8,
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
