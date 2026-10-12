package search

// MaxSets bounds the terms aggregation behind /api/v1/sets. The corpus has
// under two hundred sets; a thousand leaves room for years of releases while
// still bounding what one request can ask the cluster to bucket.
const MaxSets = 1000

// V1SetsAgg is the aggregation BuildV1SetsQuery's buckets come back under.
const V1SetsAgg = "sets"

// BuildV1SetsQuery produces the match-all request behind /api/v1/sets: one
// terms bucket per set_id (its doc_count is the set's card count) carrying one
// sample document's set fields. It is deliberately separate from
// BuildSetCatalogQuery, which the search facets and their cache own: the v1
// contract can grow without moving the UI's request, and the reverse.
func BuildV1SetsQuery() map[string]any {
	return map[string]any{
		"track_total_hits": false,
		"size":             0,
		"aggs": map[string]any{V1SetsAgg: map[string]any{
			"terms": map[string]any{"field": "set_id", "size": MaxSets},
			"aggs": map[string]any{"identity": map[string]any{"top_hits": map[string]any{
				"size":    1,
				"_source": map[string]any{"includes": []string{"set_name", "set_series", "set_total", "release_date"}},
			}}},
		}},
	}
}
