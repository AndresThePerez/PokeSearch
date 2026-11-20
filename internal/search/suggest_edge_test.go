package search

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// suggestInputs is a spread of prefixes a client can send: plain, accented,
// empty, long, and with characters that must stay valid JSON.
func suggestInputs() []string {
	return []string{
		"", "a", "pika", "Pok\u00e9mon", "pokemon", "flabebe", "fl\u00e0b\u00e9b\u00e9",
		"  padded  ", "dark alakazam", `say "hi"`, `back\slash`, "tab\there", "line\nbreak",
		"\u2028\u2029", "\x00nul", "\xff\xfe", "\U0001F525", "<script>&amp;",
		strings.Repeat("a", 256), strings.Repeat("\u00e9", 1000),
	}
}

// The completion body is the one request whose shape the fuzzy flag changes,
// and only by the one extra option. Without the flag the body has no fuzzy
// section at all; with it, the section holds the edit distance and the
// character-aware option and nothing else; the prefix always travels as given.
func TestBuildSuggestBodiesByFlag(t *testing.T) {
	for _, q := range suggestInputs() {
		plain := map[string]any{"suggest": map[string]any{"card": map[string]any{
			"prefix": q,
			"completion": map[string]any{
				"field": "name.suggest", "size": 8, "skip_duplicates": true,
			},
		}}}
		fuzzy := map[string]any{"suggest": map[string]any{"card": map[string]any{
			"prefix": q,
			"completion": map[string]any{
				"field": "name.suggest", "size": 8, "skip_duplicates": true,
				"fuzzy": map[string]any{"fuzziness": "AUTO", "unicode_aware": true},
			},
		}}}
		if got, want := canonV(t, BuildSuggest(q, false)), canonV(t, plain); got != want {
			t.Errorf("plain %+q\n got %s\nwant %s", q, got, want)
		}
		got := canonV(t, BuildSuggest(q, true))
		if want := canonV(t, fuzzy); got != want {
			t.Errorf("fuzzy %+q\n got %s\nwant %s", q, got, want)
		}
		if !json.Valid([]byte(got)) {
			t.Errorf("fuzzy %+q is not valid JSON: %s", q, got)
		}
		// A valid prefix survives a JSON round trip untouched, quotes and
		// backslashes included.
		var back struct {
			Suggest struct {
				Card struct {
					Prefix string `json:"prefix"`
				} `json:"card"`
			} `json:"suggest"`
		}
		if err := json.Unmarshal([]byte(got), &back); err != nil {
			t.Fatalf("%+q: %v", q, err)
		}
		if utf8.ValidString(q) && back.Suggest.Card.Prefix != q {
			t.Errorf("prefix %+q came back as %+q", q, back.Suggest.Card.Prefix)
		}
	}
}

// The other suggest body, the ranked one, takes no fuzzy option and must not
// pick one up: it is a prefix match, not a completion.
func TestBuildSuggestByPrintCountHasNoFuzzyOption(t *testing.T) {
	for _, q := range suggestInputs() {
		b := canonV(t, BuildSuggestByPrintCount(q))
		for _, banned := range []string{"unicode_aware", "fuzziness", "fuzzy"} {
			if strings.Contains(b, banned) {
				t.Errorf("ranked suggest for %+q mentions %q: %s", q, banned, b)
			}
		}
	}
	// The did-you-mean body is another suggester; it must not change either.
	if b := canonV(t, BuildDidYouMean("pikuchu")); strings.Contains(b, "unicode_aware") {
		t.Errorf("did-you-mean carries the option: %s", b)
	}
}

// Each call builds its own maps: a caller that edits the body it got (the
// handler marshals it, a test may tweak it) must not change the next body,
// and calls from many goroutines must not race on shared state.
func TestBuildSuggestBodiesAreNotShared(t *testing.T) {
	want := canonV(t, BuildSuggest("pokemon", true))
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				body := BuildSuggest("pokemon", true)
				fz := body["suggest"].(map[string]any)["card"].(map[string]any)["completion"].(map[string]any)["fuzzy"].(map[string]any)
				fz["fuzziness"] = "5"
				delete(fz, "unicode_aware")
				fz["extra"] = i
				if got := canonV(t, BuildSuggest("pokemon", true)); got != want {
					t.Errorf("a later body changed after an earlier one was edited: %s", got)
					return
				}
			}
		}()
	}
	wg.Wait()
	if _, has := BuildSuggest("x", false)["suggest"].(map[string]any)["card"].(map[string]any)["completion"].(map[string]any)["fuzzy"]; has {
		t.Errorf("the plain body gained a fuzzy section")
	}
}
