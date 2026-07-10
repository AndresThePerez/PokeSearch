// Package search turns HTTP query params into Elasticsearch DSL via pure
// functions — unit-testable byte-for-byte, loggable, and returnable to the
// UI query inspector.
package search

import (
	"net/url"
	"strconv"
	"strings"
)

const PageSize = 24
const MaxPage = 400 // keeps from+size inside ES's 10k result window

// CanonicalTypes are the exactly-11 TCG energy types present in the corpus.
var CanonicalTypes = []string{
	"Colorless", "Darkness", "Dragon", "Fairy", "Fighting",
	"Fire", "Grass", "Lightning", "Metal", "Psychic", "Water",
}

var canonicalSupertypes = map[string]string{
	"pokemon": "Pokémon", "trainer": "Trainer", "energy": "Energy",
}

// Params is the validated, canonicalized form of an /api/search request.
type Params struct {
	Q         string
	ID        string
	Supertype string
	Types     []string
	Rarity    []string
	Series    []string
	SetID     string
	HPMin     *int
	HPMax     *int
	Sort      string
	Order     string
	Page      int
	Debug     bool
}

// ParseParams validates url.Values per the Design Spec: invalid values are
// dropped, never rejected.
func ParseParams(v url.Values) Params {
	p := Params{
		Q:      strings.TrimSpace(v.Get("q")),
		ID:     strings.TrimSpace(v.Get("id")),
		Rarity: splitList(v.Get("rarity")),
		Series: splitList(v.Get("series")),
		SetID:  strings.TrimSpace(v.Get("set")),
		HPMin:  atoiPtr(v.Get("hp_min")),
		HPMax:  atoiPtr(v.Get("hp_max")),
		Page:   1,
		Debug:  v.Get("debug") == "1",
	}
	if canon, ok := canonicalSupertypes[strings.ToLower(strings.TrimSpace(v.Get("supertype")))]; ok {
		p.Supertype = canon
	}
	seen := map[string]bool{}
	for _, item := range splitList(v.Get("types")) {
		for _, canon := range CanonicalTypes {
			if strings.EqualFold(item, canon) && !seen[canon] {
				p.Types = append(p.Types, canon)
				seen[canon] = true
			}
		}
	}
	switch v.Get("sort") {
	case "relevance", "newest", "oldest", "hp", "name":
		p.Sort = v.Get("sort")
	default:
		if p.Q != "" {
			p.Sort = "relevance"
		} else {
			p.Sort = "newest"
		}
	}
	switch p.Sort {
	case "hp":
		p.Order = "desc"
	case "name":
		p.Order = "asc"
	}
	if o := v.Get("order"); (o == "asc" || o == "desc") && p.Order != "" {
		p.Order = o
	}
	if n, err := strconv.Atoi(v.Get("page")); err == nil {
		p.Page = min(max(n, 1), MaxPage)
	}
	return p
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func atoiPtr(s string) *int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return &n
}
