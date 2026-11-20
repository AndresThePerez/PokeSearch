package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// anyRouteES answers every route's Elasticsearch calls with a plausible body
// and counts them.
func anyRouteES(calls *atomic.Int32) roundTripperFunc {
	return func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.Path, "_explain"):
			return esResponse(200, `{"matched":false,"explanation":{"value":0.0,"description":"no match"}}`), nil
		case bytes.Contains(body, []byte(`"set_catalog"`)):
			return esResponse(200, catalogESBody), nil
		case bytes.Contains(body, []byte(`"suggest"`)):
			return esResponse(200, `{"took":1,"suggest":{"card":[{"text":"pika","offset":0,"length":4,"options":[{"text":"Pikachu"}]}]}}`), nil
		case bytes.Contains(body, []byte(`"max_hp"`)):
			return esResponse(200, statsESBody), nil
		}
		return esResponse(200, searchESBody), nil
	}
}

// hostileList is n distinct members of exactly runes characters, the number
// that tells them apart followed by unit.
func hostileList(unit string, runes, n int) []string {
	out := make([]string, n)
	for i := range out {
		id := string(rune('A'+i%26)) + string(rune('a'+i/26))
		out[i] = id + strings.Repeat(unit, (runes-2)/utf8.RuneCountInString(unit))
	}
	return out
}

// A refused request's query-log line stays under 2 KB, is one line of valid
// JSON and valid UTF-8, and carries no DSL, whatever the values are made of:
// characters that grow when escaped, characters that need four bytes,
// invalid bytes, and values built to close the line and forge another.
// Every parameter is at its largest accepted size, and an older strict field
// is what refuses the request.
func TestRefusedLogLineIsSmallAndWellFormedForHostileValues(t *testing.T) {
	units := map[string]string{
		"quote":        `"`,
		"backslash":    `\`,
		"control":      "\x01",
		"newline":      "\n",
		"html":         "<",
		"line sep":     " ",
		"4-byte":       "🜲",
		"invalid byte": "\xff",
		"half 4-byte":  "\xf0\x9f\x9c",
		"forgery":      "\"}\n{\"forged\":\"",
	}
	for name, unit := range units {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			s, logBuf := newTestServer(t, anyRouteES(&calls))
			// Bracketed by letters so a value of spaces is not trimmed away.
			within := func(limit int) string {
				return "x" + strings.Repeat(unit, (limit-2)/utf8.RuneCountInString(unit)) + "x"
			}
			one := within(search.MaxValueRunes)
			v := url.Values{
				"sort":   {"bogus"},
				"q":      {within(search.MaxQueryRunes)},
				"set":    {one},
				"id":     {one},
				"rarity": {strings.Join(hostileList(unit, search.MaxListRunes/search.MaxListMembers, search.MaxListMembers), ",")},
				"series": {strings.Join(hostileList(unit, search.MaxListRunes/search.MaxListMembers, search.MaxListMembers), ",")},
				"types":  {strings.Join(search.CanonicalTypes, ",")},
			}
			rec := getWith(t, s, "/api/search?"+v.Encode(), map[string]string{"X-Request-Id": strings.Repeat("a", 64)})
			if rec.Code != 400 {
				t.Fatalf("status %d, want 400", rec.Code)
			}
			if calls.Load() != 0 {
				t.Errorf("Elasticsearch called %d times for a refused request", calls.Load())
			}
			var queryLines []string
			for _, line := range strings.Split(strings.TrimSuffix(logBuf.String(), "\n"), "\n") {
				if !json.Valid([]byte(line)) {
					t.Fatalf("log line is not valid JSON: %.200q", line)
				}
				if !strings.Contains(line, `"msg":"access"`) {
					queryLines = append(queryLines, line)
				}
			}
			if len(queryLines) != 1 {
				t.Fatalf("want one query-log line, got %d", len(queryLines))
			}
			line := queryLines[0]
			if len(line) > maxRefusedLogLine {
				t.Errorf("query-log line is %d bytes, want at most %d: %.400s", len(line), maxRefusedLogLine, line)
			}
			if !utf8.ValidString(line) {
				t.Errorf("query-log line is not valid UTF-8")
			}
			var lg map[string]any
			if err := json.Unmarshal([]byte(line), &lg); err != nil {
				t.Fatal(err)
			}
			if _, forged := lg["forged"]; forged {
				t.Errorf("a value forged a field: %s", line)
			}
			if raw, ok := lg["dsl"]; !ok || raw != nil {
				t.Errorf("dsl = %v (present %v), want null", raw, ok)
			}
			if lg["status"] != float64(400) || lg["error"] != "sort must be one of relevance|newest|oldest|hp|name" {
				t.Errorf("status %v error %v", lg["status"], lg["error"])
			}
			p, _ := lg["params"].(map[string]any)
			for _, list := range []string{"rarity", "series"} {
				members, _ := p[list].([]any)
				if len(members) != 3 || members[2] != "[62 more]" {
					t.Errorf("logged %s = %v, want two members and \"[62 more]\"", list, p[list])
				}
			}
			for _, key := range []string{"q", "set", "id"} {
				got, _ := p[key].(string)
				if !strings.HasSuffix(got, cutLogMarker) || utf8.RuneCountInString(strings.TrimSuffix(got, cutLogMarker)) != 32 {
					t.Errorf("logged %s = %q, want 32 characters and the cut marker", key, got)
				}
			}
		})
	}
}

// A cut never splits a character: the logged value is the first 32
// characters of the original, a four-byte character at the edge kept whole.
func TestRefusedLogCutKeepsWholeCharacters(t *testing.T) {
	for _, q := range []string{
		strings.Repeat("a", 31) + "🜲🜲🜲",
		strings.Repeat("🜲", 40),
		strings.Repeat("é", 33),
		"e" + strings.Repeat("́", 40),
	} {
		s, logBuf := newTestServer(t, neverCalled(t))
		get(t, s, "/api/search?sort=bogus&q="+url.QueryEscape(q))
		p, _ := queryLine(t, logBuf)["params"].(map[string]any)
		got, _ := p["q"].(string)
		kept, cut := strings.CutSuffix(got, cutLogMarker)
		if !cut || !strings.HasPrefix(q, kept) || utf8.RuneCountInString(kept) != 32 || !utf8.ValidString(kept) {
			t.Errorf("q %q logged as %q, want its first 32 characters and the marker", q, got)
		}
	}
	// Exactly 32 characters is not cut.
	s, logBuf := newTestServer(t, neverCalled(t))
	q := strings.Repeat("🜲", 32)
	get(t, s, "/api/search?sort=bogus&q="+url.QueryEscape(q))
	if p, _ := queryLine(t, logBuf)["params"].(map[string]any); p["q"] != q {
		t.Errorf("a 32-character q was logged as %q", p["q"])
	}
}

// Only the parameters the route reads reach the log: an unknown parameter,
// even one whose name or value is 16 KB, is not written, on a refused or an
// accepted request.
func TestUnknownParametersStayOutOfTheLog(t *testing.T) {
	huge := strings.Repeat("N", 16<<10)
	for _, path := range []string{
		"/api/search?sort=bogus&" + huge + "=1",
		"/api/search?q=pika&" + huge + "=1",
		"/api/search?q=pika&x=" + huge,
		"/api/suggest?q=" + strings.Repeat("a", 300) + "&" + huge + "=1",
		"/api/explain?q=pika&id=" + strings.Repeat("a", 129) + "&" + huge + "=1",
		"/api/compare?q=pika&a=nope&" + huge + "=1",
	} {
		var calls atomic.Int32
		s, logBuf := newTestServer(t, anyRouteES(&calls))
		get(t, s, path)
		if strings.Contains(logBuf.String(), "NNNNNNNN") {
			t.Errorf("%.60s…: an unknown parameter reached the log", path)
		}
	}
}

// The new limits belong to /api/search. A route that does not read rarity,
// series or set answers exactly as it did before them: an over-limit list
// there is ignored, not refused, and the route still reaches Elasticsearch.
func TestRoutesThatIgnoreListsDoNotRefuseOverLimitLists(t *testing.T) {
	over := "&rarity=" + strings.Repeat("a,", search.MaxListMembers+1) +
		"&series=" + strings.Repeat("s", search.MaxValueRunes+1) +
		"&set=" + strings.Repeat("t", search.MaxValueRunes+1)
	for _, path := range []string{
		"/api/suggest?q=pika" + over,
		"/api/stats?debug=1" + over,
		"/api/explain?id=base1-4&q=pika" + over,
		"/api/compare?q=pika" + over,
	} {
		var calls atomic.Int32
		s, _ := newTestServer(t, anyRouteES(&calls))
		rec := get(t, s, path)
		if rec.Code != 200 || calls.Load() == 0 {
			t.Errorf("%.40s: status %d after %d upstream calls, want 200 from Elasticsearch: %.200s",
				path, rec.Code, calls.Load(), rec.Body.String())
		}
	}
}

// Every older strict-field error on /api/search is refused before the query
// is built, too: Elasticsearch untouched, the line carrying no DSL.
func TestOlderFieldErrorsAreRefusedBeforeBuilding(t *testing.T) {
	for field, value := range map[string]string{
		"sort": "bogus", "order": "up", "supertype": "item", "hp_min": "x",
		"hp_max": "1.5", "page": "two", "page_size": "lots", "q": strings.Repeat("a", 257),
	} {
		s, logBuf := newTestServer(t, neverCalled(t))
		rec := get(t, s, "/api/search?rarity=Common&series=Base&"+field+"="+value)
		if rec.Code != 400 || decodeError(t, rec).Error.Field != field {
			t.Errorf("%s=%s: status %d %s", field, value, rec.Code, rec.Body.String())
			continue
		}
		if lg := queryLine(t, logBuf); lg["dsl"] != nil || lg["status"] != float64(400) {
			t.Errorf("%s: line dsl %v status %v", field, lg["dsl"] != nil, lg["status"])
		}
	}
}

// An accepted request is logged as it always was: the parameters as
// logParams renders them and the full DSL BuildQuery made, nothing cut,
// even for values far longer than a refused line keeps.
func TestAcceptedLinesAreNeverCut(t *testing.T) {
	long := hostileList("x", search.MaxValueRunes, search.MaxListRunes/search.MaxValueRunes)
	q := strings.TrimSpace(strings.Repeat("abcdefg ", 32))
	for _, raw := range []string{
		"q=" + url.QueryEscape(q),
		"q=pikachu&debug=1&rarity=" + url.QueryEscape(strings.Join(long, ",")) +
			"&series=" + url.QueryEscape(strings.Join(long, ",")) + "&set=" + strings.Repeat("s", 128) +
			"&id=" + strings.Repeat("i", 128) + "&types=fire,water,grass&sort=name&order=desc&hp_min=10&page=2",
	} {
		var calls atomic.Int32
		s, logBuf := newTestServer(t, anyRouteES(&calls))
		rec := get(t, s, "/api/search?"+raw)
		if rec.Code != 200 {
			t.Fatalf("status %d: %.300s", rec.Code, rec.Body.String())
		}
		v, _ := url.ParseQuery(raw)
		p, errs := search.ParseParams(v)
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		roundTrip := func(x any) any {
			b, err := json.Marshal(x)
			if err != nil {
				t.Fatal(err)
			}
			var out any
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		lg := queryLine(t, logBuf)
		if !reflect.DeepEqual(lg["params"], roundTrip(logParams(p))) {
			t.Errorf("logged params differ from logParams: %.300v", lg["params"])
		}
		if !reflect.DeepEqual(lg["dsl"], roundTrip(search.BuildQuery(p))) {
			t.Errorf("logged DSL differs from BuildQuery")
		}
		if strings.Contains(logBuf.String(), "[cut]") || strings.Contains(logBuf.String(), "more]") {
			t.Errorf("an accepted line was cut")
		}
	}
}
