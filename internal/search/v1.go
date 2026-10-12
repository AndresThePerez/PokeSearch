package search

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaxCardIDs is how many distinct ids one /api/v1/cards?ids= request may name.
// It bounds the _mget a single client request can make the cluster serve; the
// limit counts distinct ids, so repeating an id never pushes a request over it.
const MaxCardIDs = 100

// CardIDPattern is the shape of a card id the v1 API accepts. It admits every
// id in the corpus, including the two Unown cards "ex10-?" and "ex10-!", and
// nothing that could not be one: a leading separator, whitespace, a slash or
// anything past 128 characters is a client mistake, and a 400 says so more
// usefully than an empty lookup.
const CardIDPattern = `^[A-Za-z0-9][A-Za-z0-9._!?-]{0,127}$`

var cardIDRe = regexp.MustCompile(CardIDPattern)

// ValidCardID reports whether id has the shape of a card id.
func ValidCardID(id string) bool {
	return cardIDRe.MatchString(id)
}

// ParseCardIDs turns the raw ids parameter into the ids to look up: split on
// commas, each trimmed, duplicates collapsed with the first occurrence keeping
// its position. Any malformed member rejects the whole list rather than being
// dropped, because a batch that silently shrinks reads as "not found".
func ParseCardIDs(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("ids is required")
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, min(len(parts), MaxCardIDs+1))
	out := make([]string, 0, min(len(parts), MaxCardIDs))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if !ValidCardID(id) {
			return nil, fmt.Errorf("ids must be a comma-separated list of card ids matching %s", CardIDPattern)
		}
		if seen[id] {
			continue
		}
		if len(out) == MaxCardIDs {
			return nil, fmt.Errorf("ids takes at most %d distinct card ids", MaxCardIDs)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// BuildCardsMget produces the _mget body for a v1 lookup. The ids travel in the
// JSON body on purpose: the ES client writes a document id into the URL path
// unescaped, so a single-document API cannot find "ex10-?" (the "?" starts a
// query string). The full _source is returned — the v1 contract is the card
// exactly as indexed.
func BuildCardsMget(ids []string) map[string]any {
	return map[string]any{"ids": append([]string{}, ids...)}
}
