package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// maxRefusedLogLine bounds the query-log line of a refused request, however
// large the request was.
const maxRefusedLogLine = 2048

// neverCalled fails the test if the handler reaches Elasticsearch.
func neverCalled(t *testing.T) roundTripperFunc {
	return func(r *http.Request) (*http.Response, error) {
		t.Errorf("a refused request reached Elasticsearch: %s %s", r.Method, r.URL.Path)
		return esResponse(200, searchESBody), nil
	}
}

// rawQueryLine returns the one query-log line exactly as written.
func rawQueryLine(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("undecodable log line: %v", err)
		}
		if m["msg"] != "access" {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly 1 query-log line, got %d", len(found))
	}
	return found[0]
}

func repeatList(member string, n int) string {
	return strings.TrimSuffix(strings.Repeat(member+",", n), ",")
}

// distinctList is n distinct members of exactly runes characters each, padded
// with fill after a number that tells them apart.
func distinctList(fill string, runes, n int) string {
	parts := make([]string, n)
	for i := range parts {
		id := strconv.Itoa(i)
		parts[i] = id + strings.Repeat(fill, runes-len(id))
	}
	return strings.Join(parts, ",")
}

// A request over a limit is answered 400 with the envelope before any query
// is built: Elasticsearch is never called, the query-log line carries no DSL,
// and the line stays small however large the request was.
func TestRefusedRequestsAreNotBuiltAndLogSmall(t *testing.T) {
	long := func(n int) string { return strings.Repeat("<", n) }
	acceptedLists := "&rarity=" + url.QueryEscape(distinctList("<", search.MaxValueRunes, search.MaxListRunes/search.MaxValueRunes)) +
		"&series=" + url.QueryEscape(distinctList("\x01", search.MaxValueRunes, search.MaxListRunes/search.MaxValueRunes))
	for _, tc := range []struct {
		name, path, field, message string
	}{
		{"search, 65 rarity members", "/api/search?rarity=" + repeatList("a", 65), "rarity", "rarity takes at most 64 values"},
		{"search, 10,000 series members", "/api/search?series=" + repeatList("a", 10000), "series", "series takes at most 64 values"},
		{"search, 129-character set", "/api/search?set=" + long(129), "set", "set must be at most 128 characters"},
		{"search, 129-character id", "/api/search?id=" + strings.Repeat("a", 129), "id", "id must be at most 128 characters"},
		{"search, long rarity member", "/api/search?rarity=" + url.QueryEscape(long(20000)), "rarity", "each rarity value must be at most 128 characters"},
		{"search, bad sort beside the largest accepted lists", "/api/search?sort=bogus&q=" + url.QueryEscape(long(256)) +
			"&set=" + url.QueryEscape(long(128)) + "&id=" + strings.Repeat("a", 128) + acceptedLists,
			"sort", "sort must be one of relevance|newest|oldest|hp|name"},
		{"search, long q beside the largest accepted lists", "/api/search?q=" + url.QueryEscape(long(20000)) + acceptedLists,
			"q", "q must be at most 256 characters"},
		{"explain, long q", "/api/explain?id=base1-4&q=" + url.QueryEscape(long(20000)), "q", "q must be at most 256 characters"},
		{"explain, long id", "/api/explain?q=pikachu&id=" + url.QueryEscape(long(20000)), "id", "id must be at most 128 characters"},
		{"compare, long q", "/api/compare?q=" + url.QueryEscape(long(20000)), "q", "q must be at most 256 characters"},
		{"compare, long profile", "/api/compare?q=pikachu&a=" + url.QueryEscape(long(20000)), "a", ""},
		{"suggest, long q", "/api/suggest?q=" + url.QueryEscape(long(20000)), "q", "q must be at most 256 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, logBuf := newTestServer(t, neverCalled(t))
			rec := get(t, s, tc.path)
			if rec.Code != 400 {
				t.Fatalf("status %d, want 400: %.300s", rec.Code, rec.Body.String())
			}
			body := decodeError(t, rec)
			if body.Error.Code != "invalid_param" || body.Error.Field != tc.field || body.Error.Message == "" ||
				(tc.message != "" && body.Error.Message != tc.message) {
				t.Errorf("400 body = %+v, want invalid_param on %q with %q", body.Error, tc.field, tc.message)
			}
			line := rawQueryLine(t, logBuf)
			if len(line) > maxRefusedLogLine {
				t.Errorf("query-log line is %d bytes for a %d-byte request, want at most %d: %.300s",
					len(line), len(tc.path), maxRefusedLogLine, line)
			}
			lg := queryLine(t, logBuf)
			if lg["dsl"] != nil {
				t.Errorf("a refused request's line carries a DSL: %.300v", lg["dsl"])
			}
			if lg["status"] != float64(400) || lg["error"] != body.Error.Message {
				t.Errorf("line status %v error %v, want 400 and %q", lg["status"], lg["error"], body.Error.Message)
			}
		})
	}
}

// A value cut for a refused request's line says that it was cut, and a value
// within the cut length is logged as given.
func TestRefusedLineMarksCutValues(t *testing.T) {
	s, logBuf := newTestServer(t, neverCalled(t))
	get(t, s, "/api/search?sort=bogus&q="+strings.Repeat("a", 256)+"&set=base1")
	p, _ := queryLine(t, logBuf)["params"].(map[string]any)
	q, _ := p["q"].(string)
	if !strings.HasPrefix(q, "aaaa") || len(q) >= 256 || !strings.HasSuffix(q, "[cut]") {
		t.Errorf("logged q = %q, want a short prefix marked as cut", q)
	}
	if p["set"] != "base1" {
		t.Errorf("logged set = %v, want base1 as given", p["set"])
	}
}

// An accepted request at the limits is built, sent and logged in full: the
// limits change nothing for a request within them.
func TestAcceptedRequestAtTheLimitsIsLoggedInFull(t *testing.T) {
	calls := 0
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		calls++
		return esResponse(200, searchESBody), nil
	})
	s, logBuf := newTestServer(t, rt)
	member := strings.Repeat("r", search.MaxValueRunes)
	members := search.MaxListRunes / search.MaxValueRunes
	rec := get(t, s, "/api/search?q=pikachu&rarity="+repeatList(member, members)+
		"&series="+repeatList(member, members)+"&set="+member)
	if rec.Code != 200 || calls != 1 {
		t.Fatalf("status %d after %d searches, want 200 after 1: %.300s", rec.Code, calls, rec.Body.String())
	}
	lg := queryLine(t, logBuf)
	if lg["dsl"] == nil || lg["status"] != float64(200) {
		t.Fatalf("line dsl %v status %v", lg["dsl"] != nil, lg["status"])
	}
	p := lg["params"].(map[string]any)
	if r, _ := p["rarity"].([]any); len(r) != 1 || r[0] != member {
		t.Errorf("logged rarity = %v, want the one member in full", p["rarity"])
	}
	if p["set"] != member {
		t.Errorf("logged set = %v, want it in full", p["set"])
	}
}

// A refused /api/v1/cards lookup logs its ids cut like any refused request,
// so its line stays small however long the list was; the lookup is never
// made.
func TestRefusedV1CardsLogsIDsCut(t *testing.T) {
	for name, ids := range map[string]string{
		"20 KB malformed id": strings.Repeat("<", 20000),
		"101 distinct ids":   distinctList("x", 64, 101),
		"bad id at the end":  distinctList("x", 64, 100) + ",/",
	} {
		t.Run(name, func(t *testing.T) {
			s, logBuf := newTestServer(t, neverCalled(t))
			rec := get(t, s, "/api/v1/cards?ids="+url.QueryEscape(ids))
			if rec.Code != 400 {
				t.Fatalf("status %d, want 400: %.300s", rec.Code, rec.Body.String())
			}
			if body := decodeError(t, rec); body.Error.Code != "invalid_param" || body.Error.Field != "ids" {
				t.Errorf("400 body = %+v, want invalid_param on ids", body.Error)
			}
			if line := rawQueryLine(t, logBuf); len(line) > maxRefusedLogLine {
				t.Errorf("query-log line is %d bytes for %d bytes of ids, want at most %d", len(line), len(ids), maxRefusedLogLine)
			}
			lg := queryLine(t, logBuf)
			p, _ := lg["params"].(map[string]any)
			if got, _ := p["ids"].(string); !strings.HasSuffix(got, cutLogMarker) ||
				!strings.HasPrefix(ids, strings.TrimSuffix(got, cutLogMarker)) {
				t.Errorf("logged ids = %.100q, want a prefix of the value with the cut marker", got)
			}
			if lg["status"] != float64(400) || lg["dsl"] != nil {
				t.Errorf("line status %v dsl %v", lg["status"], lg["dsl"] != nil)
			}
		})
	}
}
