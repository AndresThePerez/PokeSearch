package server

import (
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// A refused search builds no query. The request carries both lists at their
// largest accepted size and is refused only for its sort, so the line it logs
// looks the same either way; what differs is the work done before refusing.
// Building the query (a copy of each list into every facet's scope) roughly
// triples the allocations, so a bound at twice today's count catches it
// without depending on exact numbers.
func TestRefusedSearchBuildsNoQuery(t *testing.T) {
	s := newTestServerLogging(t, neverCalled(t), io.Discard)
	lists := search.MaxListRunes / search.MaxValueRunes
	path := "/api/search?sort=bogus&q=" + url.QueryEscape(strings.Repeat("<", search.MaxQueryRunes)) +
		"&rarity=" + url.QueryEscape(distinctList("<", search.MaxValueRunes, lists)) +
		"&series=" + url.QueryEscape(distinctList(">", search.MaxValueRunes, lists))
	allocs := testing.AllocsPerRun(50, func() {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 400 {
			t.Fatalf("status %d, want 400", rec.Code)
		}
	})
	if allocs > 200 {
		t.Errorf("refusing took %.0f allocations, want at most 200: the query is being built before the check", allocs)
	}
}
