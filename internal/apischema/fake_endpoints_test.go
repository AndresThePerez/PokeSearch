package apischema

// The fake cluster's answers for the four pre-v1 endpoints that read the index
// beyond a card search: suggest, explain, compare and stats. Like the rest of
// the fake they are canned shapes of what Elasticsearch sends, derived from the
// golden-set cards, so the real handlers decode and flatten them unchanged.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// hasMember reports whether v is a JSON object holding the named member.
func hasMember(v any, name string) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, ok = m[name]
	return ok
}

// namedBoost finds the clause carrying _name == name anywhere under v and
// returns its boost (0 when the clause has none or is absent).
func namedBoost(v any, name string) float64 {
	switch x := v.(type) {
	case map[string]any:
		if n, _ := x["_name"].(string); n == name {
			b, _ := x["boost"].(float64)
			return b
		}
		for _, child := range x {
			if b := namedBoost(child, name); b != 0 {
				return b
			}
		}
	case []any:
		for _, child := range x {
			if b := namedBoost(child, name); b != 0 {
				return b
			}
		}
	}
	return 0
}

// prefixBoost is the weight a card search body gives its prefix branch.
func prefixBoost(req map[string]any) float64 { return namedBoost(req["query"], "prefix") }

// cardNames is every distinct card name, sorted.
func (f *fakeCluster) cardNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, c := range f.cards {
		if !seen[c.Name] {
			seen[c.Name] = true
			names = append(names, c.Name)
		}
	}
	slices.Sort(names)
	return names
}

func hasWordWithPrefix(name, prefix string) bool {
	for _, w := range strings.Fields(strings.ToLower(name)) {
		if strings.HasPrefix(w, prefix) {
			return true
		}
	}
	return false
}

// rankedNames answers the print-count aggregation /api/suggest asks first: one
// bucket per distinct name that has a word starting with the typed text.
func (f *fakeCluster) rankedNames(req map[string]any) string {
	query, _ := req["query"].(map[string]any)
	mm, _ := query["multi_match"].(map[string]any)
	typed, _ := mm["query"].(string)
	typed = strings.ToLower(typed)
	var buckets []string
	for _, name := range f.cardNames() {
		if len(buckets) == search.SuggestSize || !hasWordWithPrefix(name, typed) {
			continue
		}
		buckets = append(buckets, fmt.Sprintf(`{"key":%s,"doc_count":1,"display":{"hits":{"hits":[{"_source":{"name":%s}}]}}}`,
			mustJSON(strings.ToLower(name)), mustJSON(name)))
	}
	return `{"took":2,"hits":{"hits":[]},"aggregations":{"names":{"buckets":[` + strings.Join(buckets, ",") + `]}}}`
}

// completion answers the completion suggester. The plain pass matches a name
// prefix; the fuzzy pass, which only runs when the plain one found nothing,
// stands in for an edit-distance match by taking every name that shares the
// first letter.
func (f *fakeCluster) completion(req map[string]any) string {
	card, _ := req["suggest"].(map[string]any)["card"].(map[string]any)
	prefix, _ := card["prefix"].(string)
	completion, _ := card["completion"].(map[string]any)
	fuzzy := hasMember(completion, "fuzzy")
	prefix = strings.ToLower(prefix)
	var options []string
	for _, name := range f.cardNames() {
		lower := strings.ToLower(name)
		sameStart := prefix != "" && lower != "" && []rune(lower)[0] == []rune(prefix)[0]
		if strings.HasPrefix(lower, prefix) || (fuzzy && sameStart) {
			options = append(options, fmt.Sprintf(`{"text":%s}`, mustJSON(name)))
		}
	}
	return fmt.Sprintf(`{"took":1,"hits":{"hits":[]},"suggest":{"card":[{"text":%s,"offset":0,"length":%d,"options":[%s]}]}}`,
		mustJSON(prefix), len([]rune(prefix)), strings.Join(options, ","))
}

// explain answers _explain for one document and one branch query. A document
// the index lacks is the 404 with matched:false that the handler turns into
// found:false. Otherwise the exact and prefix branches match and the other two
// do not, so a body holds matched and unmatched branches, with and without
// children.
func (f *fakeCluster) explain(id string, body []byte) (int, string) {
	if _, ok := f.cards[id]; !ok {
		return 404, fmt.Sprintf(`{"_index":"cards","_id":%s,"matched":false}`, mustJSON(id))
	}
	switch {
	case strings.Contains(string(body), `"_name":"exact"`):
		return 200, `{"_index":"cards","_id":"x","matched":true,"explanation":{"value":12.5,"description":"weight(name.kw:charizard in 0) [PerFieldSimilarity], result of:","details":[{"value":1.25,"description":"idf"},{"value":8,"description":"boost"}]}}`
	case strings.Contains(string(body), `"_name":"prefix"`):
		return 200, `{"_index":"cards","_id":"x","matched":true,"explanation":{"value":3.25,"description":"sum of:","details":[]}}`
	}
	return 200, `{"_index":"cards","_id":"x","matched":false,"explanation":{"value":0.0,"description":"no matching term","details":[]}}`
}

// stats answers the corpus aggregation /api/stats runs, from the cards the
// cluster holds: a year histogram with its empty years kept, 30-point HP bands,
// the maximum HP, and the four categorical breakdowns.
func (f *fakeCluster) stats() string {
	years, bands := map[int]int{}, map[int]int{}
	types, supertypes, rarities, series := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	maxHP, hasHP := 0, false
	first, last := 0, 0
	for _, c := range f.cards {
		y, _ := strconv.Atoi(c.ReleaseDate[:4])
		years[y]++
		if first == 0 || y < first {
			first = y
		}
		last = max(last, y)
		if c.HP != nil {
			bands[*c.HP/30*30]++
			maxHP, hasHP = max(maxHP, *c.HP), true
		}
		for _, t := range c.Types {
			types[t]++
		}
		supertypes[c.Supertype]++
		if c.Rarity != "" {
			rarities[c.Rarity]++
		}
		series[c.SetSeries]++
	}
	terms := func(m map[string]int) string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var buckets []string
		for _, k := range keys {
			buckets = append(buckets, fmt.Sprintf(`{"key":%s,"doc_count":%d}`, mustJSON(k), m[k]))
		}
		return `{"buckets":[` + strings.Join(buckets, ",") + `]}`
	}
	var perYear []string
	for y := first; first != 0 && y <= last; y++ {
		perYear = append(perYear, fmt.Sprintf(`{"key_as_string":"%04d-01-01T00:00:00.000Z","key":0,"doc_count":%d}`, y, years[y]))
	}
	var hp []string
	for from := 0; hasHP && from <= maxHP; from += 30 {
		hp = append(hp, fmt.Sprintf(`{"key":%d.0,"doc_count":%d}`, from, bands[from]))
	}
	maxValue := "null"
	if hasHP {
		maxValue = strconv.Itoa(maxHP) + ".0"
	}
	return fmt.Sprintf(`{"took":3,"timed_out":false,"hits":{"total":{"value":%d,"relation":"eq"},"hits":[]},"aggregations":{"per_year":{"buckets":[%s]},"hp":{"buckets":[%s]},"max_hp":{"value":%s},"types":%s,"supertype":%s,"rarity":%s,"series":%s}}`,
		len(f.cards), strings.Join(perYear, ","), strings.Join(hp, ","), maxValue,
		terms(types), terms(supertypes), terms(rarities), terms(series))
}
