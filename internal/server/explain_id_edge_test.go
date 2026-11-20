package server

import (
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// newTestServerAt is newTestServer against an explicit cluster address, for
// the cases where the address itself (a path prefix, userinfo) is under test.
func newTestServerAt(t *testing.T, address string, rt http.RoundTripper, logW io.Writer) *Server {
	t.Helper()
	es, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{address}, Transport: rt})
	if err != nil {
		t.Fatal(err)
	}
	fixed := func() time.Time { return time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC) }
	return New(es, fstest.MapFS{}, logW, fixed)
}

// explainTarget is what the cluster reads off one _explain request.
type explainTarget struct{ path, escapedPath, rawQuery, fragment string }

// recordExplains answers every _explain as a missing document and records
// where each request went.
func recordExplains(mu *sync.Mutex, seen *[]explainTarget) roundTripperFunc {
	return func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		*seen = append(*seen, explainTarget{r.URL.Path, r.URL.EscapedPath(), r.URL.RawQuery, r.URL.Fragment})
		mu.Unlock()
		return esResponse(http.StatusNotFound, `{"_index":"cards","_id":"x","matched":false}`), nil
	}
}

// checkExplainedAs asks /api/explain for id and requires every outgoing call
// to name exactly that document as one path segment, with no query string.
func checkExplainedAs(t *testing.T, s *Server, mu *sync.Mutex, seen *[]explainTarget, prefix, id string) {
	t.Helper()
	mu.Lock()
	*seen = nil
	mu.Unlock()
	rec := get(t, s, "/api/explain?q=charizard&id="+url.QueryEscape(id))
	if rec.Code != http.StatusOK {
		t.Fatalf("id %q: status %d, want 200: %s", id, rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*seen) != 4 {
		t.Fatalf("id %q: %d explain calls, want 4", id, len(*seen))
	}
	for i, got := range *seen {
		if got.path != prefix+"/cards/_explain/"+id || got.rawQuery != "" || got.fragment != "" {
			t.Errorf("id %q call %d: path %q query %q fragment %q, want the id as the last segment and no query",
				id, i, got.path, got.rawQuery, got.fragment)
		}
		segment := strings.TrimPrefix(got.escapedPath, prefix+"/cards/_explain/")
		if strings.ContainsAny(segment, "/?#") {
			t.Errorf("id %q call %d: escaped segment %q would split the path", id, i, segment)
		}
		if decoded, err := url.PathUnescape(segment); err != nil || decoded != id {
			t.Errorf("id %q call %d: escaped segment %q decodes to %q (%v)", id, i, segment, decoded, err)
		}
	}
}

// Every id the card-id pattern accepts reaches Elasticsearch as itself: dots,
// bangs and question marks in any position and at the length limit.
func TestExplainEveryAcceptedIDIsOnePathSegment(t *testing.T) {
	var mu sync.Mutex
	var seen []explainTarget
	s, _ := newTestServer(t, recordExplains(&mu, &seen))
	ids := []string{
		"a", "a.", "a..b", "a...", "a!", "ex10-!", "ex10-?", "a?b-c", "a??", "a?pretty",
		"a.?!-_", "A0_-.!?", "9?", "base1-4?pretty", "a" + strings.Repeat("?", 127), strings.Repeat("a", 128),
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._!?-"
	rng := rand.New(rand.NewSource(20261120))
	for range 200 {
		b := []byte{alphabet[rng.Intn(62)]}
		for range rng.Intn(128) {
			b = append(b, alphabet[rng.Intn(len(alphabet))])
		}
		ids = append(ids, string(b))
	}
	for _, id := range ids {
		if !search.ValidCardID(id) {
			t.Fatalf("test id %q is not one the pattern accepts", id)
		}
		checkExplainedAs(t, s, &mu, &seen, "", id)
	}
}

// A cluster behind a path prefix gets the prefix and the escaped id together;
// the "?" of ex10-? is still part of the id, not a query string.
func TestExplainIDUnderAnAddressPathPrefix(t *testing.T) {
	var mu sync.Mutex
	var seen []explainTarget
	s := newTestServerAt(t, "http://fake-es:9200/es", recordExplains(&mu, &seen), io.Discard)
	for _, id := range []string{"ex10-?", "base1-4?pretty", "ex10-!", "a..b"} {
		checkExplainedAs(t, s, &mu, &seen, "/es", id)
	}
}

// Ids the pattern rejects never reach the cluster, however they are encoded,
// and are named as the id parameter.
func TestExplainRejectsEveryIDOutsideThePattern(t *testing.T) {
	var calls atomic.Int32
	s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return esResponse(http.StatusOK, `{"matched":false}`), nil
	}))
	// raw is the query-string value as sent, so the encodings are the test.
	for _, raw := range []string{
		"..", ".", "%2e%2e", "%252e%252e", "a%2Fb", "a%252Fb", "a/b", "a%20b", "a%00", "a%23b",
		"a%26b%3Dc", "%2Fetc", "-x", "_x", "!x", "%3Fx", strings.Repeat("a", 129),
		"%C3%A9", "a%C3%A9", "%EF%BC%A1", "%E2%84%AA", "a%0Ab", "a%09b", "a%25", "a%2Cb", "a~b", "a%5Cb",
	} {
		calls.Store(0)
		rec := get(t, s, "/api/explain?q=charizard&id="+raw)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("id=%s: status %d, want 400: %s", raw, rec.Code, rec.Body.String())
			continue
		}
		if body := decodeError(t, rec); body.Error.Code != codeInvalidParam || body.Error.Field != "id" {
			t.Errorf("id=%s: error %+v, want invalid_param on id", raw, body.Error)
		}
		if calls.Load() != 0 {
			t.Errorf("id=%s: %d ES calls, want none", raw, calls.Load())
		}
	}
}

// Empty and whitespace-only ids are the existing "id is required" answer; an
// id given twice is read as its first value, like every other parameter.
func TestExplainIDPresenceAndRepetition(t *testing.T) {
	var calls atomic.Int32
	s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return esResponse(http.StatusOK, `{"matched":false}`), nil
	}))
	for _, raw := range []string{"", "%20%20", "%09"} {
		calls.Store(0)
		rec := get(t, s, "/api/explain?q=charizard&id="+raw)
		body := decodeError(t, rec)
		if rec.Code != http.StatusBadRequest || body.Error.Field != "id" || body.Error.Message != "id is required" || calls.Load() != 0 {
			t.Errorf("id=%q: %d %+v after %d calls, want 400 id is required and no call", raw, rec.Code, body.Error, calls.Load())
		}
	}

	var mu sync.Mutex
	var seen []explainTarget
	twice, _ := newTestServer(t, recordExplains(&mu, &seen))
	rec := get(t, twice, "/api/explain?q=charizard&id=base1-4&id=a%2Fb")
	if rec.Code != http.StatusOK || len(seen) != 4 || seen[0].path != "/cards/_explain/base1-4" {
		t.Errorf("id given twice: %d, calls %v, want the first id explained", rec.Code, seen)
	}
}

// When id and q are both wrong, explain names id, every time.
func TestExplainNamesIDBeforeQ(t *testing.T) {
	s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Error("an invalid request reached ES")
		return esResponse(http.StatusOK, `{}`), nil
	}))
	long := url.QueryEscape(strings.Repeat("a", search.MaxQueryRunes+1))
	for _, path := range []string{
		"/api/explain?id=a%2Fb&q=" + long,
		"/api/explain?q=" + long + "&id=-x",
		"/api/explain?id=a%20b",
		"/api/explain?id=" + strings.Repeat("a", 129) + "&q=" + long,
	} {
		for range 3 {
			rec := get(t, s, path)
			if body := decodeError(t, rec); rec.Code != http.StatusBadRequest || body.Error.Field != "id" {
				t.Errorf("%s: %d %+v, want 400 on id", path, rec.Code, body.Error)
			}
		}
	}
}

// The q cap counts characters, not bytes, and terms by any Unicode space; a
// whitespace-only q keeps each endpoint's existing answer; q given twice is
// its first value. None of the rejected requests costs an ES round trip.
func TestQueryCapEdgesOnSuggestExplainCompare(t *testing.T) {
	emoji := "\U0001F525" // four bytes in UTF-8
	ideoSpace := "　"      // ideographic space
	nbsp := " "           // no-break space, a space to unicode.IsSpace
	terms := func(n int, sep string) string { return strings.TrimSuffix(strings.Repeat("x"+sep, n), sep) }
	cases := []struct {
		name string
		q    string
		over bool
	}{
		{"256 four-byte characters", strings.Repeat(emoji, search.MaxQueryRunes), false},
		{"257 four-byte characters", strings.Repeat(emoji, search.MaxQueryRunes+1), true},
		{"256 characters inside padding", "   " + strings.Repeat("é", search.MaxQueryRunes) + "\t ", false},
		{"32 terms split by ideographic spaces", terms(search.MaxQueryTerms, ideoSpace), false},
		{"33 terms split by ideographic spaces", terms(search.MaxQueryTerms+1, ideoSpace), true},
		{"33 terms split by no-break spaces", terms(search.MaxQueryTerms+1, nbsp), true},
		{"33 terms split by mixed whitespace", terms(search.MaxQueryTerms+1, " \t\n"), true},
	}
	endpoints := []struct {
		name string
		path func(q string) string
	}{
		{"suggest", func(q string) string { return "/api/suggest?q=" + url.QueryEscape(q) }},
		{"explain", func(q string) string { return "/api/explain?id=base1-4&q=" + url.QueryEscape(q) }},
		{"compare", func(q string) string { return "/api/compare?q=" + url.QueryEscape(q) }},
	}
	for _, ep := range endpoints {
		for _, c := range cases {
			t.Run(ep.name+"/"+c.name, func(t *testing.T) {
				var calls atomic.Int32
				s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return esResponse(http.StatusInternalServerError, `{}`), nil
				}))
				rec := get(t, s, ep.path(c.q))
				if !c.over {
					if rec.Code == http.StatusBadRequest || calls.Load() == 0 {
						t.Fatalf("a q within the cap was rejected: %d after %d calls: %s", rec.Code, calls.Load(), rec.Body.String())
					}
					return
				}
				body := decodeError(t, rec)
				if rec.Code != http.StatusBadRequest || body.Error.Field != "q" || calls.Load() != 0 {
					t.Errorf("%d %+v after %d calls, want 400 on q and no call", rec.Code, body.Error, calls.Load())
				}
			})
		}
		t.Run(ep.name+"/q given twice", func(t *testing.T) {
			var calls atomic.Int32
			s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return esResponse(http.StatusInternalServerError, `{}`), nil
			}))
			rec := get(t, s, ep.path("pika")+"&q="+url.QueryEscape(strings.Repeat("a", search.MaxQueryRunes+1)))
			if rec.Code == http.StatusBadRequest || calls.Load() == 0 {
				t.Errorf("the first q is the one read: %d after %d calls: %s", rec.Code, calls.Load(), rec.Body.String())
			}
		})
	}

	// Whitespace only: suggest's empty answer, the existing required error
	// on explain and compare, and no round trip on any of them.
	var calls atomic.Int32
	s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return esResponse(http.StatusOK, `{}`), nil
	}))
	blank := url.QueryEscape(" \t" + ideoSpace + " ")
	if rec := get(t, s, "/api/suggest?q="+blank); rec.Code != http.StatusOK || rec.Body.String() != "{\"suggestions\":[]}\n" {
		t.Errorf("suggest on a blank q: %d %q", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/explain?id=base1-4&q=" + blank, "/api/compare?q=" + blank} {
		rec := get(t, s, path)
		if body := decodeError(t, rec); rec.Code != http.StatusBadRequest || body.Error.Field != "q" || body.Error.Message != "q is required" {
			t.Errorf("%s: %d %+v, want 400 q is required", path, rec.Code, body.Error)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("blank queries made %d ES calls, want none", calls.Load())
	}
}

// FuzzExplainIDPath holds the escaping property for arbitrary input: a value
// the pattern accepts reaches the cluster as exactly that one path segment,
// and anything else is refused before a round trip.
func FuzzExplainIDPath(f *testing.F) {
	for _, seed := range []string{"ex10-?", "base1-4?pretty", "a..b", "a/b", "..", "a%3Fb", "a#b", "é"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, id string) {
		var mu sync.Mutex
		var seen []explainTarget
		s, _ := newTestServer(t, recordExplains(&mu, &seen))
		trimmed := strings.TrimSpace(id)
		if !search.ValidCardID(trimmed) {
			rec := get(t, s, "/api/explain?q=charizard&id="+url.QueryEscape(id))
			if rec.Code != http.StatusBadRequest || len(seen) != 0 {
				t.Fatalf("id %q: %d after %d calls, want 400 and none", id, rec.Code, len(seen))
			}
			return
		}
		checkExplainedAs(t, s, &mu, &seen, "", trimmed)
	})
}
