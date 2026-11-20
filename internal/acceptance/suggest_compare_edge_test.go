//go:build acceptance

package acceptance

import (
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// The queries the comparison tests ask about: a name, a name with many prints,
// a rare name, a word that matches text and not a name, a typo and a prefix.
var compareQueries = []string{"pikachu", "charizard", "unown", "mew", "trainer", "fire energy", "pikuchu", "pika", "zzzzqqqqxxxx"}

func compareBetween(t *testing.T, q, a, b string) compareResp {
	t.Helper()
	return compare(t, "q="+url.QueryEscape(q)+"&a="+a+"&b="+b)
}

// Each delta reports the positions its card has in the two windows above it, and
// its status says which way it moved: nothing in the movement list is invented
// beyond what the two lists show.
func TestCompareDeltasAreTheTwoWindowsRead(t *testing.T) {
	profiles := []string{"served", "previous", "runner-up"}
	checked := 0
	for _, q := range compareQueries {
		for _, a := range profiles {
			for _, b := range profiles {
				r := compareBetween(t, q, a, b)
				rankIn := func(s compareSide) map[string]int {
					m := map[string]int{}
					for _, e := range s.Results {
						m[e.ID] = e.Rank
					}
					return m
				}
				ranksA, ranksB := rankIn(r.A), rankIn(r.B)
				seen := map[string]bool{}
				for _, d := range r.Deltas {
					checked++
					if seen[d.ID] {
						t.Errorf("%s %s/%s: %s appears twice in the deltas", q, a, b, d.ID)
					}
					seen[d.ID] = true
					ra, inA := ranksA[d.ID]
					rb, inB := ranksB[d.ID]
					if (d.RankA != nil) != inA || (d.RankB != nil) != inB || !inA && !inB {
						t.Errorf("%s %s/%s: delta %+v disagrees with the windows (in a: %v, in b: %v)", q, a, b, d, inA, inB)
						continue
					}
					if inA && *d.RankA != ra || inB && *d.RankB != rb {
						t.Errorf("%s %s/%s: delta %+v, windows say rank_a %d rank_b %d", q, a, b, d, ra, rb)
					}
					if !inA || !inB {
						continue
					}
					moved := ra - rb
					status := "same"
					if moved > 0 {
						status = "up"
					} else if moved < 0 {
						status = "down"
					}
					if d.Delta == nil || *d.Delta != moved || d.Status != status {
						t.Errorf("%s %s/%s: delta %+v, want %s by %d", q, a, b, d, status, moved)
					}
				}
				for id := range ranksA {
					if !seen[id] {
						t.Errorf("%s %s/%s: %s is ranked in a and missing from the deltas", q, a, b, id)
					}
				}
				for id := range ranksB {
					if !seen[id] {
						t.Errorf("%s %s/%s: %s is ranked in b and missing from the deltas", q, a, b, id)
					}
				}
			}
		}
	}
	if checked < 200 {
		t.Errorf("only %d deltas were read", checked)
	}
}

// Swapping the two profiles swaps the sides and mirrors the movement: up is
// down, entered is dropped, every delta changes sign, and the windows themselves
// are unchanged by which side they were asked for on.
func TestCompareSwapMirrorsTheMovement(t *testing.T) {
	mirror := map[string]string{"up": "down", "down": "up", "same": "same", "entered": "dropped", "dropped": "entered"}
	for _, q := range compareQueries {
		ab := compareBetween(t, q, "served", "runner-up")
		ba := compareBetween(t, q, "runner-up", "served")
		if ab.A.Profile != ba.B.Profile || ab.B.Profile != ba.A.Profile {
			t.Errorf("%s: profiles %s/%s then %s/%s", q, ab.A.Profile, ab.B.Profile, ba.A.Profile, ba.B.Profile)
		}
		if !slices.Equal(ab.A.Results, ba.B.Results) || !slices.Equal(ab.B.Results, ba.A.Results) {
			t.Errorf("%s: a window depends on the side it was asked for", q)
		}
		byID := map[string]compareDelta{}
		for _, d := range ba.Deltas {
			byID[d.ID] = d
		}
		if len(byID) != len(ab.Deltas) {
			t.Errorf("%s: %d deltas swapped for %d", q, len(ab.Deltas), len(byID))
		}
		for _, d := range ab.Deltas {
			m, ok := byID[d.ID]
			if !ok || m.Status != mirror[d.Status] {
				t.Errorf("%s: %+v is %+v when swapped, want status %s", q, d, m, mirror[d.Status])
				continue
			}
			if (d.Delta == nil) != (m.Delta == nil) || d.Delta != nil && *d.Delta != -*m.Delta {
				t.Errorf("%s: %s moves %v then %v: not mirrored", q, d.ID, d.Delta, m.Delta)
			}
		}
	}
}

// A profile set against itself moves nothing: every card is the same and the
// correlation is 1 whenever there is one to compute.
func TestCompareAProfileAgainstItselfMovesNothing(t *testing.T) {
	for _, q := range compareQueries {
		for _, p := range []string{"served", "previous", "runner-up"} {
			status, _, body := fetchRaw(t, http.MethodGet, "/api/compare?q="+url.QueryEscape(q)+"&a="+p+"&b="+p)
			if status != http.StatusOK {
				t.Fatalf("%s %s: status %d: %s", q, p, status, body)
			}
			var r struct {
				Deltas []compareDelta `json:"deltas"`
				Rho    *float64       `json:"spearman_rho_union"`
			}
			if err := json.Unmarshal(body, &r); err != nil {
				t.Fatal(err)
			}
			for _, d := range r.Deltas {
				if d.Status != "same" || d.Delta == nil || *d.Delta != 0 {
					t.Errorf("%s %s against itself: %+v", q, p, d)
				}
			}
			if r.Rho != nil && *r.Rho != 1 {
				t.Errorf("%s %s against itself: correlation %v, want 1 or null", q, p, *r.Rho)
			}
			if len(r.Deltas) >= 2 && r.Rho == nil {
				// All ranks distinct on both sides, so the correlation exists.
				t.Errorf("%s %s against itself: %d cards and a null correlation", q, p, len(r.Deltas))
			}
		}
	}
}

// The served side is what the page serves: its list is the first ten results
// of the same search and its total is the search's total, so "served" cannot
// drift from the ranking people see.
func TestCompareServedSideIsTheSearchPage(t *testing.T) {
	for _, q := range compareQueries {
		r := compareBetween(t, q, "served", "previous")
		page := search(t, "q="+url.QueryEscape(q)+"&page_size=10")
		if r.A.Total != page.Total {
			t.Errorf("%s: served total %d, search total %d", q, r.A.Total, page.Total)
		}
		var served []string
		for _, e := range r.A.Results {
			served = append(served, e.ID)
		}
		if want := ids(page.Results); !slices.Equal(served, want) {
			t.Errorf("%s: served side %v, search page %v", q, served, want)
		}
	}
}

// The comparison's score for a card is the score explain reconstructs for it:
// two endpoints describing one ranking agree to the rounding of three decimals.
func TestCompareServedScoresAreWhatExplainReports(t *testing.T) {
	checked := 0
	for _, q := range []string{"pikachu", "charizard", "mew"} {
		r := compareBetween(t, q, "served", "previous")
		for _, e := range r.A.Results[:min(3, len(r.A.Results))] {
			status, _, body := fetchRaw(t, http.MethodGet, "/api/explain?id="+url.QueryEscape(e.ID)+"&q="+url.QueryEscape(q))
			if status != http.StatusOK {
				t.Fatalf("explain %s %s: %d %s", e.ID, q, status, body)
			}
			var x explainResp
			if err := json.Unmarshal(body, &x); err != nil {
				t.Fatal(err)
			}
			checked++
			if !x.Found || math.Abs(x.Score-e.Score) > 0.005 {
				t.Errorf("%s %s: compare scores %v, explain %v (found %v)", q, e.ID, e.Score, x.Score, x.Found)
			}
		}
	}
	if checked < 9 {
		t.Errorf("only %d cards compared", checked)
	}
}

// The 256-character cap on q is the same number on every endpoint that reads
// it, counted in characters (never bytes) after the padding is trimmed: 256 is
// an answer, 257 is the 400 naming q. Characters of one, two, three and four
// bytes, and bytes that are not UTF-8 at all, count one each.
func TestQueryCapIsTheSameOnSuggestExplainAndCompare(t *testing.T) {
	endpoints := []struct {
		name string
		path func(q string) string
	}{
		{"suggest", func(q string) string { return "/api/suggest?q=" + q }},
		{"explain", func(q string) string { return "/api/explain?id=base1-4&q=" + q }},
		{"compare", func(q string) string { return "/api/compare?q=" + q }},
	}
	units := []struct{ name, unit string }{
		{"one byte", "a"}, {"two bytes", "%C3%A9"}, {"three bytes", "%E2%82%AC"}, {"four bytes", "%F0%9F%98%80"}, {"invalid byte", "%FF"},
	}
	for _, ep := range endpoints {
		for _, u := range units {
			for _, n := range []int{256, 257} {
				q := strings.Repeat(u.unit, n)
				status, _, body := fetchRaw(t, http.MethodGet, ep.path(q))
				want := http.StatusOK
				if n == 257 {
					want = http.StatusBadRequest
				}
				if status != want {
					t.Errorf("%s with %d x %s: status %d, want %d: %.160s", ep.name, n, u.name, status, want, body)
					continue
				}
				if n == 257 {
					var e errorResp
					if err := json.Unmarshal(body, &e); err != nil || e.Error.Code != "invalid_param" || e.Error.Field != "q" {
						t.Errorf("%s with %d x %s: error %s", ep.name, n, u.name, body)
					}
				}
			}
			// Padding does not count towards the cap.
			padded := "%20%C2%A0%E3%80%80" + strings.Repeat(u.unit, 256) + "%20%C2%A0"
			if status, _, body := fetchRaw(t, http.MethodGet, ep.path(padded)); status != http.StatusOK {
				t.Errorf("%s with 256 x %s and padding: status %d: %.160s", ep.name, u.name, status, body)
			}
		}
	}
}

// Spaces around a typed prefix are not part of it: suggest answers for the
// trimmed text, whatever kind of space it is padded with.
func TestSuggestIgnoresPaddingAroundThePrefix(t *testing.T) {
	want := suggest(t, "pika")
	if len(want) == 0 {
		t.Fatal("pika completed to nothing")
	}
	for _, padded := range []string{"%20pika", "pika%20", "%20%20pika%20%20", "%C2%A0pika%C2%A0", "%E3%80%80pika", "%09pika%0A"} {
		if got := suggest(t, padded); !slices.Equal(got, want) {
			t.Errorf("q=%s gave %v, pika gave %v", padded, got, want)
		}
	}
}

// Explain echoes the query it scored, trimmed, and says so for an id that the
// index does not hold in exactly the shape of one it does: the same branches in
// the same order, so a client can render either without a second code path.
func TestExplainAnswersAMissingCardWithTheBranchesOfAPresentOne(t *testing.T) {
	get := func(id string) explainResp {
		status, _, body := fetchRaw(t, http.MethodGet, "/api/explain?id="+url.QueryEscape(id)+"&q=%20charizard%20")
		if status != http.StatusOK {
			t.Fatalf("explain %s: %d %s", id, status, body)
		}
		var x explainResp
		if err := json.Unmarshal(body, &x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	present, missing := get("base1-4"), get("base1-4999")
	if present.Q != "charizard" || missing.Q != "charizard" {
		t.Errorf("q echoed as %q and %q, want the trimmed charizard", present.Q, missing.Q)
	}
	if !present.Found || missing.Found || missing.Score != 0 {
		t.Errorf("found %v/%v and missing score %v", present.Found, missing.Found, missing.Score)
	}
	if len(present.Branches) == 0 || len(present.Branches) != len(missing.Branches) {
		t.Fatalf("%d branches present, %d missing", len(present.Branches), len(missing.Branches))
	}
	for i := range present.Branches {
		if present.Branches[i].Name != missing.Branches[i].Name || missing.Branches[i].Matched || missing.Branches[i].Score != 0 {
			t.Errorf("branch %d: %+v for a present card, %+v for a missing one", i, present.Branches[i], missing.Branches[i])
		}
	}
}
