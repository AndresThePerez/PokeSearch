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

// FieldError names one rejected request parameter. It is the payload of the
// M3 error contract's 400 responses.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ParseParams validates url.Values and returns the canonicalized Params
// alongside any field errors.
//
// Superseding the M1 Design Spec's "invalid values are dropped, never
// rejected" rule (see Design Spec Milestone 3 §A2, decision D1), the strict
// fields — sort, order, supertype, and the numerics hp_min/hp_max/page/
// page_size — report a FieldError when supplied non-empty and invalid, and the
// caller turns that into a 400. Everything else stays lenient: unknown query
// keys are ignored, and unknown members of the types/rarity/series comma-lists
// are dropped. Out-of-range integers are clamped rather than rejected — a
// clamp is a contract, an alphabetic page is a typo.
func ParseParams(v url.Values) (Params, []FieldError) {
	var errs []FieldError
	p := Params{
		Q:      strings.TrimSpace(v.Get("q")),
		ID:     strings.TrimSpace(v.Get("id")),
		Rarity: splitList(v.Get("rarity")),
		Series: splitList(v.Get("series")),
		SetID:  strings.TrimSpace(v.Get("set")),
		Page:   1,
		Debug:  v.Get("debug") == "1",
	}

	if raw := v.Get("sort"); raw != "" {
		switch raw {
		case "relevance", "newest", "oldest", "hp", "name":
			p.Sort = raw
		default:
			errs = append(errs, FieldError{"sort", "sort must be one of relevance|newest|oldest|hp|name"})
		}
	}
	if p.Sort == "" {
		if p.Q != "" {
			p.Sort = "relevance"
		} else {
			p.Sort = "newest"
		}
	}
	if p.Sort == "relevance" && p.Q == "" {
		p.Sort = "newest" // match-all makes _score meaningless
	}
	switch p.Sort {
	case "hp":
		p.Order = "desc"
	case "name":
		p.Order = "asc"
	}
	// order is validated always but applied only to hp/name sorts, so
	// order=asc alongside sort=newest stays valid-and-ignored.
	if raw := v.Get("order"); raw != "" {
		switch {
		case raw != "asc" && raw != "desc":
			errs = append(errs, FieldError{"order", "order must be one of asc|desc"})
		case p.Order != "":
			p.Order = raw
		}
	}

	if raw := strings.TrimSpace(v.Get("supertype")); raw != "" {
		if canon, ok := canonicalSupertypes[strings.ToLower(raw)]; ok {
			p.Supertype = canon
		} else {
			errs = append(errs, FieldError{"supertype", "supertype must be one of pokemon|trainer|energy"})
		}
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

	if n, ok := atoiStrict(v.Get("hp_min")); ok {
		p.HPMin = n
	} else {
		errs = append(errs, FieldError{"hp_min", "hp_min must be an integer"})
	}
	if n, ok := atoiStrict(v.Get("hp_max")); ok {
		p.HPMax = n
	} else {
		errs = append(errs, FieldError{"hp_max", "hp_max must be an integer"})
	}

	if n, ok := atoiStrict(v.Get("page")); !ok {
		errs = append(errs, FieldError{"page", "page must be an integer"})
	} else if n != nil {
		p.Page = min(max(*n, 1), MaxPage)
	}
	if _, ok := atoiStrict(v.Get("page_size")); !ok {
		errs = append(errs, FieldError{"page_size", "page_size must be an integer"})
	}

	return p, errs
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

// atoiStrict parses an optional integer parameter. An absent or empty value is
// valid-and-unset (nil, true); a non-integer value is a client error
// (nil, false). Range clamping is the caller's business.
func atoiStrict(s string) (*int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil, false
	}
	return &n, true
}
