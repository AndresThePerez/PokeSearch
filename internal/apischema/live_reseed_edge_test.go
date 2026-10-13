//go:build acceptance

package apischema

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Edge tests for the reseeded stack (T49). They ask the live index about the
// corpus the pinned ref is supposed to hold, not about the old one.

const (
	reseedRef  = "39a26a144c8b6ef6c2fb17b2c29d0bb7121e3a11"
	reseedDocs = 20635
	reseedSets = 176
)

type liveSearch struct {
	Total   int `json:"total"`
	Pages   int `json:"pages"`
	Results []struct {
		ID string `json:"id"`
	} `json:"results"`
}

func liveGet(t *testing.T, path string, hdr http.Header) answer {
	t.Helper()
	return fetch(t, liveBase(), path, hdr)
}

func liveJSON(t *testing.T, path string, into any) {
	t.Helper()
	a := liveGet(t, path, http.Header{})
	if a.status != 200 {
		t.Fatalf("GET %s = %d: %s", path, a.status, a.body)
	}
	if err := json.Unmarshal(a.body, into); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

func setIDs(t *testing.T, set string) []string {
	t.Helper()
	var ids []string
	for page := 1; ; page++ {
		var s liveSearch
		liveJSON(t, fmt.Sprintf("/api/search?set=%s&page=%d", url.QueryEscape(set), page), &s)
		for _, r := range s.Results {
			ids = append(ids, r.ID)
		}
		if page >= s.Pages {
			if len(ids) != s.Total {
				t.Fatalf("set %s: paged %d ids, total says %d", set, len(ids), s.Total)
			}
			return ids
		}
	}
}

// Every card of the sets the reseed added (me5, me55, me55c) is served by the
// v1 card endpoint with an answer that satisfies card-response.json and names
// the card that was asked for.
func TestReseedAddedSetsServeEveryCardAndMatchTheSchema(t *testing.T) {
	schemas := loadSchemas(t)
	want := map[string]int{"me5": 120, "me55": 161, "me55c": 30}
	for set, n := range want {
		ids := setIDs(t, set)
		if len(ids) != n {
			t.Errorf("set %s has %d cards, want %d", set, len(ids), n)
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				t.Errorf("set %s lists %s twice", set, id)
			}
			seen[id] = true
			a := liveGet(t, "/api/v1/cards/"+url.PathEscape(id), http.Header{})
			if a.status != 200 {
				t.Errorf("GET card %s = %d", id, a.status)
				continue
			}
			if bad, err := schemas.violations("card-response.json", a.body); err != nil || len(bad) > 0 {
				t.Errorf("card %s violates schema: %v %v", id, bad, err)
			}
			var c struct {
				Data struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(a.body, &c); err != nil || c.Data.ID != id {
				t.Errorf("card %s answered data.id %q (%v)", id, c.Data.ID, err)
			}
		}
	}
}

// The set list, the facet catalog and the health count all describe one
// corpus: 176 sets whose card_count sums to 20635, each equal to a direct
// set query's total. A wrong (old) seed has 173 sets / 20324 cards.
func TestReseedSetListSumsToTheCorpusAndMatchesSearch(t *testing.T) {
	var sets struct {
		Data []struct {
			ID        string `json:"id"`
			CardCount int    `json:"card_count"`
		} `json:"data"`
	}
	liveJSON(t, "/api/v1/sets", &sets)
	if len(sets.Data) != reseedSets {
		t.Fatalf("/api/v1/sets lists %d sets, want %d", len(sets.Data), reseedSets)
	}
	sum := 0
	for _, s := range sets.Data {
		sum += s.CardCount
		var r liveSearch
		liveJSON(t, "/api/search?set="+url.QueryEscape(s.ID), &r)
		if r.Total != s.CardCount {
			t.Errorf("set %s: card_count %d, search total %d", s.ID, s.CardCount, r.Total)
		}
	}
	if sum != reseedDocs {
		t.Errorf("card_count sum = %d, want %d", sum, reseedDocs)
	}
	var h map[string]any
	liveJSON(t, "/healthz", &h)
	if len(h) != 2 || h["status"] != "ok" || h["docs"] != float64(reseedDocs) {
		t.Errorf("/healthz = %v, want exactly {docs:%d,status:ok}", h, reseedDocs)
	}
}

// Seed provenance is the same story in /api/meta, in every v1 card answer and
// in the batch answer: the pinned ref, a parseable non-null seeded_at, and a
// build stamp that is not the unstamped default.
func TestReseedProvenanceAgreesAcrossMetaAndV1(t *testing.T) {
	var meta struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
		Built   string `json:"built"`
		Docs    int    `json:"docs"`
		Seed    struct {
			Ref string  `json:"ref"`
			At  *string `json:"seeded_at"`
		} `json:"seed"`
	}
	liveJSON(t, "/api/meta", &meta)
	if meta.Seed.Ref != reseedRef || meta.Docs != reseedDocs {
		t.Fatalf("meta ref/docs = %s/%d, want %s/%d", meta.Seed.Ref, meta.Docs, reseedRef, reseedDocs)
	}
	if meta.Seed.At == nil {
		t.Fatal("meta seed.seeded_at is null on a seeded index")
	}
	seededAt, err := time.Parse(time.RFC3339, *meta.Seed.At)
	if err != nil || seededAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("seeded_at %q: parse err %v or in the future", *meta.Seed.At, err)
	}
	if meta.Commit == "" || meta.Commit == "none" || meta.Version == "" || meta.Version == "dev" {
		t.Errorf("unstamped build: commit %q version %q", meta.Commit, meta.Version)
	}
	for _, path := range []string{"/api/v1/cards/me55c-100", "/api/v1/cards?ids=me55c-100,ex10-%3F"} {
		var env struct {
			Seed struct {
				Ref string  `json:"ref"`
				At  *string `json:"seeded_at"`
			} `json:"seed"`
		}
		liveJSON(t, path, &env)
		if env.Seed.Ref != reseedRef || env.Seed.At == nil || *env.Seed.At != *meta.Seed.At {
			t.Errorf("%s seed = %+v, want ref %s at %s", path, env.Seed, reseedRef, *meta.Seed.At)
		}
	}
}

// Conditional GET on reseeded cards: the ETag round-trips to a 304 with no
// body for a new-set id and for the ids a URL path mangles; a different tag
// still gets the full 200; and the ETag differs card to card.
func TestReseedCardETagRevalidatesTo304(t *testing.T) {
	tags := map[string]string{}
	for _, id := range []string{"me55c-100", "ex10-%3F", "ex10-%21", "base1-4"} {
		path := "/api/v1/cards/" + id
		first := liveGet(t, path, http.Header{})
		if first.status != 200 {
			t.Fatalf("GET %s = %d", path, first.status)
		}
		tag := first.header.Get("ETag")
		if tag == "" {
			t.Fatalf("GET %s: no ETag", path)
		}
		if prev, dup := tags[tag]; dup {
			t.Errorf("%s and %s share ETag %s", id, prev, tag)
		}
		tags[tag] = id
		again := liveGet(t, path, http.Header{"If-None-Match": {tag}})
		if again.status != 304 || len(again.body) != 0 {
			t.Errorf("%s with matching If-None-Match = %d (%d body bytes), want 304 empty", path, again.status, len(again.body))
		}
		other := liveGet(t, path, http.Header{"If-None-Match": {`W/"0000000000000000"`}})
		if other.status != 200 || len(other.body) == 0 {
			t.Errorf("%s with a stale tag = %d, want 200 with body", path, other.status)
		}
	}
}

// A mixed batch of new-set ids, the two awkward ids and an unknown id answers
// in request order, collapses a repeat, reports the unknown id, and satisfies
// cards-batch-response.json.
func TestReseedBatchMixesNewSetsAndAwkwardIDs(t *testing.T) {
	schemas := loadSchemas(t)
	ids := "me55c-100,ex10-%3F,me5-1,me55c-100,ex10-%21,me55-158,zz99-1"
	a := liveGet(t, "/api/v1/cards?ids="+ids, http.Header{})
	if a.status != 200 {
		t.Fatalf("batch = %d: %s", a.status, a.body)
	}
	if bad, err := schemas.violations("cards-batch-response.json", a.body); err != nil || len(bad) > 0 {
		t.Errorf("batch violates schema: %v %v", bad, err)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal(a.body, &out); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range out.Data {
		got = append(got, c.ID)
	}
	if want := "me55c-100,ex10-?,me5-1,ex10-!,me55-158"; strings.Join(got, ",") != want {
		t.Errorf("batch ids = %v, want %s in request order", got, want)
	}
	if len(out.Missing) != 1 || out.Missing[0] != "zz99-1" {
		t.Errorf("missing = %v, want exactly [zz99-1]", out.Missing)
	}
}

// Prices are never written to Elasticsearch, so no reseeded card answer, from
// the largest new set to the awkward ids, may carry a price-shaped member.
func TestReseedCardsCarryNoPriceFields(t *testing.T) {
	var walk func(o any, path string, bad *[]string)
	walk = func(o any, path string, bad *[]string) {
		switch v := o.(type) {
		case map[string]any:
			for k, c := range v {
				l := strings.ToLower(k)
				if strings.Contains(l, "price") || strings.Contains(l, "tcgplayer") || strings.Contains(l, "cardmarket") || strings.Contains(l, "market") {
					*bad = append(*bad, path+"/"+k)
				}
				walk(c, path+"/"+k, bad)
			}
		case []any:
			for _, c := range v {
				walk(c, path+"[]", bad)
			}
		}
	}
	for _, id := range append(setIDs(t, "me55c"), "ex10-%3F", "base1-4") {
		a := liveGet(t, "/api/v1/cards/"+strings.ReplaceAll(url.PathEscape(id), "%253F", "%3F"), http.Header{})
		var doc any
		if err := json.Unmarshal(a.body, &doc); err != nil || a.status != 200 {
			t.Fatalf("card %s: %d %v", id, a.status, err)
		}
		var bad []string
		walk(doc, "", &bad)
		if len(bad) > 0 {
			t.Errorf("card %s carries price-shaped members: %v", id, bad)
		}
	}
}
