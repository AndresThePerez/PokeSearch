package search

import "testing"

// /api/v1/sets reads the whole set list in one size:0 aggregation: one bucket
// per set_id, each carrying a single sample document's set fields. The body is
// pinned byte for byte, like every other builder here.
func TestBuildV1SetsQueryGolden(t *testing.T) {
	got := canonV(t, BuildV1SetsQuery())
	want := canonS(t, `{
	  "track_total_hits": false, "size": 0,
	  "aggs": {"sets": {
	    "terms": {"field": "set_id", "size": 1000},
	    "aggs": {"identity": {"top_hits": {"size": 1,
	      "_source": {"includes": ["set_name", "set_series", "set_total", "release_date"]}}}}
	  }}
	}`)
	if got != want {
		t.Errorf("v1 sets DSL\n got %s\nwant %s", got, want)
	}
	if MaxSets != 1000 {
		t.Errorf("MaxSets = %d, want 1000", MaxSets)
	}
}
