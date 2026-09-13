//go:build relevance

package relevance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/esindex"
)

// mappingV2Out is where TestWriteMappingV2 puts the create-index body. The
// shadow index is created from a file rather than from a pipeline on purpose:
// an empty or truncated body still creates the index, with a default mapping,
// and still answers 200, so a create whose only success condition is the
// status code cannot tell a correct index from a silent one — and the next
// step would reindex twenty thousand documents into the wrong analysis chain.
// A file can be measured before it is sent.
const mappingV2Out = "/tmp/cards_v2-mapping.json"

// TestWriteMappingV2 is a writer, not an assertion about Elasticsearch: it
// needs no cluster and makes no request. It lives under the relevance tag with
// the rest of this package because it exists to feed the shadow-index build.
func TestWriteMappingV2(t *testing.T) {
	if err := os.WriteFile(mappingV2Out, []byte(esindex.MappingV2), 0o644); err != nil {
		t.Fatalf("write %s: %v", mappingV2Out, err)
	}
	info, err := os.Stat(mappingV2Out)
	if err != nil {
		t.Fatalf("stat %s: %v", mappingV2Out, err)
	}
	if info.Size() == 0 {
		t.Fatalf("%s is empty", mappingV2Out)
	}
	t.Logf("wrote %s (%d bytes) for index %q", mappingV2Out, info.Size(), esindex.IndexNameV2)
}

// analyzeName runs one string through the analyzer the shadow index actually
// installed on a field. It asks by field rather than by naming the analyzer,
// so what is asserted is what a document indexed into cards_v2 is broken into
// — not what a chain posted alongside the request would have produced.
func analyzeName(t *testing.T, field, text string) []string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"field": field, "text": text})
	if err != nil {
		t.Fatalf("encode analyze request: %v", err)
	}
	endpoint := fmt.Sprintf("%s/%s/_analyze", esURL(), esindex.IndexNameV2)
	res, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST %s: %v", endpoint, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", endpoint, err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d: %s", endpoint, res.StatusCode, body)
	}
	var out struct {
		Tokens []struct {
			Token string `json:"token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", endpoint, err)
	}
	tokens := make([]string, 0, len(out.Tokens))
	for _, tok := range out.Tokens {
		tokens = append(tokens, tok.Token)
	}
	return tokens
}

// canonTokens renders a token stream for byte-for-byte comparison, the way the
// query tests render a DSL body. Prose about "the tokens are folded and split"
// is not a test; a literal that has to match exactly is.
func canonTokens(t *testing.T, tokens []string) string {
	t.Helper()
	b, err := json.Marshal(tokens)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAnalyzeNameTokenStreams pins the whole chain on the field a searcher
// types into. Every want below is what the shadow index emits, not what the
// engine default emitted: the default lowercased and stopped there.
func TestAnalyzeNameTokenStreams(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			// The hyphenated suffix form, 601 cards of it. The tokenizer
			// splits the hyphen; the synonyms add the other spelling of the
			// suffix so "charizard ex" reaches this card too.
			name: "hyphenated GX suffix",
			text: "Charizard-GX",
			want: []string{"charizard", "gx", "ex"},
		},
		{
			// A tag-team name: the ampersand is not a term, both Pokemon are,
			// and the suffix still resolves.
			name: "tag team with ampersand",
			text: "Charizard & Braixen-GX",
			want: []string{"charizard", "braixen", "gx", "ex"},
		},
		{
			// The accent pair, and the reason asciifolding is in the chain.
			name: "accented",
			text: "Pokémon",
			want: []string{"pokemon"},
		},
		{
			name: "unaccented",
			text: "pokemon",
			want: []string{"pokemon"},
		},
		{
			// The modern spacing-and-lowercase spelling of the suffix, 915
			// cards of it, which the synonyms tie back to -GX and -EX.
			name: "lowercase spaced ex suffix",
			text: "Charizard ex",
			want: []string{"charizard", "ex", "gx"},
		},
		{
			// The older uppercase hyphenated spelling lands on the same two
			// suffix terms as the lowercase one: that is the synonym group
			// doing its job, not the lowercase filter.
			name: "hyphenated EX suffix",
			text: "Charizard-EX",
			want: []string{"charizard", "ex", "gx"},
		},
		{
			// A hyphen inside a name rather than before a suffix. It still
			// splits — the tokenizer does that — and nothing conflates it
			// with a suffix form.
			name: "hyphen inside a name",
			text: "Ho-Oh",
			want: []string{"ho", "oh"},
		},
		{
			// What the delimiter filter is actually here for: the tokenizer
			// keeps lv.x whole, and preserve_original keeps it whole beside
			// the parts.
			name: "dotted level suffix",
			text: "Charizard G LV.X",
			want: []string{"charizard", "g", "lv.x", "lv", "x"},
		},
		{
			// split_on_numerics is off, so the species name stays one term.
			name: "numeric inside a name",
			text: "Porygon2",
			want: []string{"porygon2"},
		},
		{
			// split_on_case_change is off, so the suffix is not three terms.
			name: "case change inside a suffix",
			text: "Charizard VMAX",
			want: []string{"charizard", "vmax"},
		},
		{
			// The possessive is stemmed and the bare owner kept beside it.
			name: "possessive owner prefix",
			text: "Ethan's Ho-Oh ex",
			want: []string{"ethan's", "ethan", "ho", "oh", "ex", "gx"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := analyzeName(t, "name", tc.text)
			t.Logf("%q -> %s", tc.text, canonTokens(t, got))
			if canonTokens(t, got) != canonTokens(t, tc.want) {
				t.Errorf("analyze %q\n got %s\nwant %s",
					tc.text, canonTokens(t, got), canonTokens(t, tc.want))
			}
		})
	}
}

// TestAnalyzeFoldsTheAccentGap is the pair on its own, because it is the one
// claim the shadow index exists to demonstrate. The served index answers these
// two queries with two different terms; here they are one term, so a searcher
// who cannot type an accent and a searcher who can reach the same cards.
func TestAnalyzeFoldsTheAccentGap(t *testing.T) {
	accented := analyzeName(t, "name", "Pokémon Center Lady")
	plain := analyzeName(t, "name", "Pokemon Center Lady")
	t.Logf("accented -> %s", canonTokens(t, accented))
	t.Logf("plain    -> %s", canonTokens(t, plain))
	if canonTokens(t, accented) != canonTokens(t, plain) {
		t.Errorf("the accent gap is still open\n accented %s\n plain    %s",
			canonTokens(t, accented), canonTokens(t, plain))
	}
	want := `["pokemon","center","lady"]`
	if got := canonTokens(t, accented); got != want {
		t.Errorf("accented stream\n got %s\nwant %s", got, want)
	}
}

// TestAnalyzeKeywordSideFolds covers the third change. name.kw is a keyword
// field, so it has a normalizer rather than an analyzer, and the exact-name
// branch of the query scores off it — an unfolded normalizer would leave that
// branch answering the accented spelling only, however well the text field
// folded.
func TestAnalyzeKeywordSideFolds(t *testing.T) {
	got := canonTokens(t, analyzeName(t, "name.kw", "Pokémon Center Lady"))
	want := `["pokemon center lady"]`
	t.Logf("name.kw -> %s", got)
	if got != want {
		t.Errorf("name.kw normalizer\n got %s\nwant %s", got, want)
	}
}
