// Package search turns HTTP query params into Elasticsearch DSL via pure
// functions — unit-testable byte-for-byte, loggable, and returnable to the
// UI query inspector.
package search

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PageSize is the default page size; MinPageSize/MaxPageSize bound what a
// client may ask for via page_size.
const PageSize = 24
const MinPageSize, MaxPageSize = 1, 100

// MaxDocsWindow is the deepest reachable document, chosen so from+size stays
// inside ES's 10k result window at every allowed page size. 9600/24 = 400
// preserves the browse contract of exactly 400 pages at the default size.
const MaxDocsWindow = 9600

// MaxPageFor is the highest requestable page at a given page size. It is the
// ceiling of MaxDocsWindow/pageSize, the same count the search response
// advertises as pages, so the last advertised page is always reachable; the
// last page then ends at most pageSize-1 documents past the window, 9,699 at
// worst, still inside ES's 10,000.
func MaxPageFor(pageSize int) int {
	if pageSize < MinPageSize {
		pageSize = MinPageSize
	}
	return (MaxDocsWindow + pageSize - 1) / pageSize
}

// MaxQueryRunes and MaxQueryTerms bound the free-text q: characters (runes,
// not bytes) and whitespace-separated terms, both counted after trimming. A
// longer query is a client error rather than an expensive request.
const MaxQueryRunes, MaxQueryTerms = 256, 32

// CheckQueryLength reports a field error on q when the trimmed query is over
// either limit, and nil when it is within both.
func CheckQueryLength(q string) *FieldError {
	if utf8.RuneCountInString(q) > MaxQueryRunes {
		return &FieldError{"q", "q must be at most " + strconv.Itoa(MaxQueryRunes) + " characters"}
	}
	if len(strings.Fields(q)) > MaxQueryTerms {
		return &FieldError{"q", "q must be at most " + strconv.Itoa(MaxQueryTerms) + " terms"}
	}
	return nil
}

// CanonicalTypes are the exactly-11 TCG energy types present in the corpus.
var CanonicalTypes = []string{
	"Colorless", "Darkness", "Dragon", "Fairy", "Fighting",
	"Fire", "Grass", "Lightning", "Metal", "Psychic", "Water",
}

// CanonicalRarities are the corpus's 44 rarity values, spelled exactly as the
// index stores them (a terms aggregation on the rarity field). The rarity
// filter matches these bytes, so the list is what other spellings of a value
// are mapped onto.
var CanonicalRarities = []string{
	"ACE SPEC Rare", "Amazing Rare", "Black White Rare", "Classic Collection",
	"Common", "Double Rare", "Futuristic Rare", "Holo Rare V",
	"Holo Rare VMAX", "Holo Rare VSTAR", "Hyper Rare", "Illustration Rare",
	"LEGEND", "MEGA_ATTACK_RARE", "Mega Hyper Rare", "Pikachu Rare",
	"Promo", "Radiant Rare", "Rare", "Rare ACE",
	"Rare BREAK", "Rare Holo", "Rare Holo EX", "Rare Holo GX",
	"Rare Holo LV.X", "Rare Holo Star", "Rare Holo V", "Rare Holo VMAX",
	"Rare Holo VSTAR", "Rare Holo ex", "Rare Prime", "Rare Prism Star",
	"Rare Rainbow", "Rare Secret", "Rare Shining", "Rare Shiny",
	"Rare Shiny GX", "Rare Ultra", "Shiny Rare", "Shiny Ultra Rare",
	"Special Illustration Rare", "Trainer Gallery Rare Holo", "Ultra Rare", "Uncommon",
}

// raritiesByFold groups CanonicalRarities by their folded spelling. A fold
// that holds two values ("Rare Holo EX" and "Rare Holo ex") names neither.
var raritiesByFold = func() map[string][]string {
	m := map[string][]string{}
	for _, r := range CanonicalRarities {
		m[foldRarity(r)] = append(m[foldRarity(r)], r)
	}
	return m
}()

// foldRarity lowercases a rarity and collapses its whitespace, so spellings
// that differ only in letter case and spacing compare equal.
func foldRarity(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// canonicalRarity returns the stored spelling of a trimmed rarity member: the
// one value it folds to, or the member verbatim when it folds to none or to
// more than one. An exact value is therefore returned as given, including
// each half of the pair that shares a fold.
func canonicalRarity(item string) string {
	if vals := raritiesByFold[foldRarity(item)]; len(vals) == 1 {
		return vals[0]
	}
	return item
}

// parseRarity maps each member of the rarity list onto its stored spelling and
// drops repeats, keeping the first position. Unknown members are kept: the
// filter then matches nothing, as it always has, rather than widening.
func parseRarity(s string) []string {
	var out []string
	for _, item := range splitList(s) {
		if item = canonicalRarity(item); !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

// typeAliases maps the labels the page shows for two energy types to the
// corpus's canonical names.
var typeAliases = map[string]string{"steel": "Metal", "normal": "Colorless"}

var canonicalSupertypes = map[string]string{
	"pokemon": "Pokémon", "pokémon": "Pokémon", "trainer": "Trainer", "energy": "Energy",
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
	PageSize  int // always MinPageSize..MaxPageSize; defaults to PageSize
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
// caller turns that into a 400. q is strict only about its length (see
// CheckQueryLength). Everything else stays lenient: unknown query
// keys are ignored, unknown members of the types list are dropped, and rarity
// members are mapped onto a stored spelling where exactly one exists and kept
// as given otherwise. Out-of-range integers are clamped rather than rejected — a
// clamp is a contract, an alphabetic page is a typo.
func ParseParams(v url.Values) (Params, []FieldError) {
	var errs []FieldError
	p := Params{
		Q:      strings.TrimSpace(v.Get("q")),
		ID:     strings.TrimSpace(v.Get("id")),
		Rarity: parseRarity(v.Get("rarity")),
		Series: splitList(v.Get("series")),
		SetID:  strings.TrimSpace(v.Get("set")),
		Page:   1,
		Debug:  v.Get("debug") == "1" || v.Get("debug") == "true",
	}

	if raw := strings.TrimSpace(v.Get("sort")); raw != "" {
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
	if raw := strings.TrimSpace(v.Get("order")); raw != "" {
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
		if alias, ok := typeAliases[strings.ToLower(item)]; ok {
			item = alias
		}
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

	// page_size is resolved before page, because it decides the page ceiling.
	p.PageSize = PageSize
	if n, ok := atoiStrict(v.Get("page_size")); !ok {
		errs = append(errs, FieldError{"page_size", "page_size must be an integer"})
	} else if n != nil {
		p.PageSize = min(max(*n, MinPageSize), MaxPageSize)
	}
	if n, ok := atoiStrict(v.Get("page")); !ok {
		errs = append(errs, FieldError{"page", "page must be an integer"})
	} else if n != nil {
		p.Page = min(max(*n, 1), MaxPageFor(p.PageSize))
	}

	// Checked last, so a request that is invalid for another reason reports
	// that field first.
	if fe := CheckQueryLength(p.Q); fe != nil {
		errs = append(errs, *fe)
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
