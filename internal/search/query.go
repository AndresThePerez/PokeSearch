package search

import "strings"

// BuildQuery produces the complete _search body for a validated Params value.
// Keeping this pure makes the generated ES DSL byte-testable and loggable.
func BuildQuery(p Params) map[string]any {
	return map[string]any{
		"track_total_hits": true,
		"from":             (p.Page - 1) * PageSize,
		"size":             PageSize,
		"query":            map[string]any{"bool": buildBool(p)},
		"sort":             buildSort(p),
		"aggs": map[string]any{
			"supertype":  map[string]any{"terms": map[string]any{"field": "supertype", "size": 3}},
			"types":      map[string]any{"terms": map[string]any{"field": "types", "size": 11}},
			"rarity":     map[string]any{"terms": map[string]any{"field": "rarity", "size": 30}},
			"set_series": map[string]any{"terms": map[string]any{"field": "set_series", "size": 20}},
		},
	}
}

func buildBool(p Params) map[string]any {
	b := map[string]any{}
	if p.Q == "" {
		b["must"] = []any{map[string]any{"match_all": map[string]any{}}}
	} else {
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
	}

	if filter := buildFilters(p); len(filter) > 0 {
		b["filter"] = filter
	}
	return b
}

func buildFilters(p Params) []any {
	var filter []any
	if p.ID != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"id": p.ID}})
	}
	if p.Supertype != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"supertype": p.Supertype}})
	}
	if len(p.Types) > 0 {
		filter = append(filter, map[string]any{"terms": map[string]any{"types": p.Types}})
	}
	if len(p.Rarity) > 0 {
		filter = append(filter, map[string]any{"terms": map[string]any{"rarity": p.Rarity}})
	}
	if len(p.Series) > 0 {
		filter = append(filter, map[string]any{"terms": map[string]any{"set_series": p.Series}})
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
