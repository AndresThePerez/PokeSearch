package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// queryOfRunes and queryOfTerms build the boundary inputs of the q cap.
func queryOfRunes(n int) string { return strings.Repeat("a", n) }

func queryOfTerms(n int) string {
	return strings.TrimSpace(strings.Repeat("x ", n))
}

// The q cap /api/search applies holds on every endpoint that takes a q: the
// same field, the same message, and no Elasticsearch round trip for a query
// over either limit. A query exactly at a limit still reaches the cluster.
func TestEdgeQueryCapOnSuggestExplainCompare(t *testing.T) {
	routes := []struct {
		name string
		path func(q string) string
	}{
		{"suggest", func(q string) string { return "/api/suggest?q=" + url.QueryEscape(q) }},
		{"explain", func(q string) string { return "/api/explain?id=base1-4&q=" + url.QueryEscape(q) }},
		{"compare", func(q string) string { return "/api/compare?q=" + url.QueryEscape(q) }},
	}
	inputs := []struct {
		label string
		q     string
		over  bool
	}{
		{"256 characters", queryOfRunes(search.MaxQueryRunes), false},
		{"257 characters", queryOfRunes(search.MaxQueryRunes + 1), true},
		{"256 accented characters", strings.Repeat("é", search.MaxQueryRunes), false},
		{"32 terms", queryOfTerms(search.MaxQueryTerms), false},
		{"33 terms", queryOfTerms(search.MaxQueryTerms + 1), true},
	}
	for _, route := range routes {
		for _, in := range inputs {
			t.Run(route.name+"/"+in.label, func(t *testing.T) {
				var calls atomic.Int32
				rt := roundTripperFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return nil, errors.New("connection refused")
				})
				s, logBuf := newTestServer(t, rt)
				rec := get(t, s, route.path(in.q))
				if !in.over {
					if calls.Load() == 0 || rec.Code != http.StatusServiceUnavailable {
						t.Fatalf("a q within the cap must reach ES: status %d after %d calls: %s",
							rec.Code, calls.Load(), rec.Body.String())
					}
					return
				}
				if calls.Load() != 0 {
					t.Errorf("an over-long q made %d ES calls, want none", calls.Load())
				}
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
				}
				body := decodeError(t, rec)
				want := search.CheckQueryLength(in.q)
				if body.Error.Code != codeInvalidParam || body.Error.Field != "q" || body.Error.Message != want.Message {
					t.Errorf("error = %+v, want invalid_param on q with %q", body.Error, want.Message)
				}
				searchBody := decodeError(t, get(t, s, "/api/search?q="+url.QueryEscape(in.q)))
				if body.Error.Message != searchBody.Error.Message {
					t.Errorf("message %q differs from /api/search's %q", body.Error.Message, searchBody.Error.Message)
				}
				if !strings.Contains(logBuf.String(), `"status":400`) {
					t.Errorf("the rejection must be logged: %s", logBuf.String())
				}
			})
		}
	}
}

// es400 answers every call the way Elasticsearch rejects a query it cannot
// parse, with a reason that must never reach a client.
func es400(calls *atomic.Int32) roundTripperFunc {
	return func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return esResponse(400, `{"error":{"root_cause":[{"type":"query_shard_exception","reason":"`+edgeSentinel+`"}],
		  "type":"search_phase_execution_exception","reason":"`+edgeSentinel+`"},"status":400}`), nil
	}
}

// A 400 from Elasticsearch on one of the four query endpoints is the client's
// request being rejected, not the cluster being down: 400 invalid_param, a
// message that carries nothing of the cluster's reply, the reply in the log.
func TestEdgeES400IsAClientErrorOnTheQueryEndpoints(t *testing.T) {
	for _, path := range []string{
		"/api/search?q=charizard",
		"/api/search?q=charizard&sort=hp",
		"/api/suggest?q=pika",
		"/api/explain?id=base1-4&q=charizard",
		"/api/compare?q=charizard",
	} {
		t.Run(path, func(t *testing.T) {
			var calls atomic.Int32
			s, logBuf := newTestServer(t, es400(&calls))
			rec := get(t, s, path)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
			}
			body := decodeError(t, rec)
			if body.Error.Code != codeInvalidParam || body.Error.Field != "" || body.Error.Message == "" {
				t.Errorf("error = %+v, want invalid_param with no field and a message", body.Error)
			}
			for _, leak := range []string{edgeSentinel, "exception", "shard", "elasticsearch"} {
				if strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(leak)) {
					t.Errorf("client body carries %q: %s", leak, rec.Body.String())
				}
			}
			if body.RequestID == "" || body.RequestID != rec.Header().Get("X-Request-Id") {
				t.Errorf("request_id %q, header %q", body.RequestID, rec.Header().Get("X-Request-Id"))
			}
			lg := queryLine(t, logBuf)
			if lg["status"] != float64(400) || !strings.Contains(lg["error"].(string), edgeSentinel) {
				t.Errorf("the cause must reach the query line with status 400: %v", lg)
			}
			if calls.Load() != 1 {
				t.Errorf("%d ES calls, want the one that was rejected", calls.Load())
			}
		})
	}
}

// The same 400 on every other route keeps the behaviour clients already
// handle: 503 es_unavailable on v1, meta and stats, the frozen body on healthz.
func TestEdgeES400KeepsOtherRoutesAt503(t *testing.T) {
	for _, path := range []string{
		"/api/v1/cards/base1-4",
		"/api/v1/cards?ids=base1-4,base1-2",
		"/api/v1/sets",
		"/api/meta",
		"/api/stats",
	} {
		t.Run(path, func(t *testing.T) {
			var calls atomic.Int32
			s, _ := newTestServer(t, es400(&calls))
			rec := get(t, s, path)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status %d, want 503: %s", rec.Code, rec.Body.String())
			}
			if body := decodeError(t, rec); body.Error.Code != codeESUnavailable ||
				body.Error.Message != "elasticsearch unavailable" {
				t.Errorf("error = %+v, want the es_unavailable contract", body.Error)
			}
		})
	}
	t.Run("/healthz", func(t *testing.T) {
		var calls atomic.Int32
		s, _ := newTestServer(t, es400(&calls))
		rec := get(t, s, "/healthz")
		if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"status\":\"error\"}\n" {
			t.Errorf("healthz = %d %q, want 503 and the frozen body", rec.Code, rec.Body.String())
		}
	})
}

// A 500 is still the cluster failing, on the query endpoints as everywhere.
func TestEdgeES500StaysA503OnTheQueryEndpoints(t *testing.T) {
	for _, path := range []string{
		"/api/search?q=charizard", "/api/suggest?q=pika",
		"/api/explain?id=base1-4&q=charizard", "/api/compare?q=charizard",
	} {
		s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return esResponse(500, `{"error":{"type":"illegal_state_exception"}}`), nil
		}))
		rec := get(t, s, path)
		if rec.Code != http.StatusServiceUnavailable || decodeError(t, rec).Error.Code != codeESUnavailable {
			t.Errorf("%s: %d %s, want the 503 contract", path, rec.Code, rec.Body.String())
		}
	}
}

// A malformed explain id is the client's mistake, named before any ES call.
func TestEdgeExplainRejectsMalformedIDs(t *testing.T) {
	s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("a malformed id reached ES: %s", r.URL.String())
		return esResponse(200, `{}`), nil
	}))
	for _, id := range []string{"a b", "x/y", "-x", ".x", "a%b", "a#b", "a&b", strings.Repeat("a", 129)} {
		rec := get(t, s, "/api/explain?q=charizard&id="+url.QueryEscape(id))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("id %q: status %d, want 400: %s", id, rec.Code, rec.Body.String())
			continue
		}
		if body := decodeError(t, rec); body.Error.Code != codeInvalidParam || body.Error.Field != "id" || body.Error.Message == "" {
			t.Errorf("id %q: error %+v, want invalid_param on id", id, body.Error)
		}
	}
	// The longest id the pattern admits is still explained.
	var calls atomic.Int32
	ok, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return esResponse(404, `{"_index":"cards","_id":"x","matched":false}`), nil
	}))
	if rec := get(t, ok, "/api/explain?q=charizard&id="+strings.Repeat("a", 128)); rec.Code != 200 || calls.Load() != 4 {
		t.Errorf("a 128-character id: %d after %d calls, want 200 after 4", rec.Code, calls.Load())
	}
}

// explainedRequest is what the fake cluster saw of one _explain call.
type explainedRequest struct{ escapedPath, rawQuery string }

// pathKeyedExplain answers _explain by the document id the cluster would
// actually read off the request: only base1-4 exists. It records each call.
func pathKeyedExplain(mu *sync.Mutex, seen *[]explainedRequest) roundTripperFunc {
	return func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		*seen = append(*seen, explainedRequest{r.URL.EscapedPath(), r.URL.RawQuery})
		mu.Unlock()
		switch r.URL.Path {
		case "/cards/_explain/base1-4", "/cards/_explain/ex10-?":
			return esResponse(200, `{"matched":true,"explanation":{"value":12.5,"description":"sum of:"}}`), nil
		default:
			return esResponse(404, `{"_index":"cards","_id":"other","matched":false}`), nil
		}
	}
}

// A valid id reaches Elasticsearch as one path segment whatever it holds: a
// "?" is part of the id, never the start of a query string, so ex10-? is
// explained and base1-4?pretty is a card that does not exist rather than
// base1-4 with a formatting flag.
func TestEdgeExplainIDReachesESIntact(t *testing.T) {
	cases := []struct {
		id, wantPath string
		found        bool
	}{
		{"ex10-?", "/cards/_explain/ex10-%3F", true},
		{"base1-4?pretty", "/cards/_explain/base1-4%3Fpretty", false},
		{"base1-4", "/cards/_explain/base1-4", true},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			var mu sync.Mutex
			var seen []explainedRequest
			s, _ := newTestServer(t, pathKeyedExplain(&mu, &seen))
			rec := get(t, s, "/api/explain?q=charizard&id="+url.QueryEscape(c.id))
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var resp struct {
				ID    string  `json:"id"`
				Found bool    `json:"found"`
				Score float64 `json:"score"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			wantScore := 0.0
			if c.found {
				wantScore = 50 // four matched branches at 12.5
			}
			if resp.ID != c.id || resp.Found != c.found || resp.Score != wantScore {
				t.Errorf("response = %+v, want id %q found %v score %v", resp, c.id, c.found, wantScore)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 4 {
				t.Fatalf("%d explain calls, want 4: %v", len(seen), seen)
			}
			for i, req := range seen {
				if req.escapedPath != c.wantPath || req.rawQuery != "" {
					t.Errorf("call %d went to %q with query %q, want %q and no query", i, req.escapedPath, req.rawQuery, c.wantPath)
				}
			}
		})
	}
}

// A missing index is the cluster not being able to answer, which is a 503;
// only a missing document is the found:false answer.
func TestEdgeExplainMissingIndexIs503(t *testing.T) {
	s, logBuf := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return esResponse(404, `{"error":{"root_cause":[{"type":"index_not_found_exception","reason":"no such index [cards]",
		  "index":"cards"}],"type":"index_not_found_exception","reason":"no such index [cards]","index":"cards"},"status":404}`), nil
	}))
	rec := get(t, s, "/api/explain?id=base1-4&q=charizard")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); body.Error.Code != codeESUnavailable {
		t.Errorf("error = %+v, want es_unavailable", body.Error)
	}
	if lg := queryLine(t, logBuf); lg["status"] != float64(503) ||
		!strings.Contains(lg["error"].(string), "index_not_found_exception") {
		t.Errorf("the cause must reach the log: %v", lg)
	}
}

// The document-missing reply Elasticsearch actually sends for _explain keeps
// the found:false answer, and healthz on a missing index is still the
// not-seeded-yet signal, byte for byte.
func TestEdgeExplainMissingDocumentAndHealthzMissingIndex(t *testing.T) {
	s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return esResponse(404, `{"_index":"cards","_id":"nope-1","matched":false}`), nil
	}))
	rec := get(t, s, "/api/explain?id=nope-1&q=charizard")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"found":false`) {
		t.Errorf("missing document: %d %s, want 200 found:false", rec.Code, rec.Body.String())
	}

	h, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return esResponse(404, `{"error":{"type":"index_not_found_exception","reason":"no such index [cards]"},"status":404}`), nil
	}))
	if rec := get(t, h, "/healthz"); rec.Code != 200 || rec.Body.String() != "{\"docs\":0,\"status\":\"ok\"}\n" {
		t.Errorf("healthz on a missing index = %d %q", rec.Code, rec.Body.String())
	}
}

// A failing healthz keeps its frozen body and status and logs one line that
// says why, tied to the request by its id.
func TestEdgeHealthzLogsItsCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rt    roundTripperFunc
		cause string
	}{
		{"refused", func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial tcp 192.0.2.10:9200: connect: connection refused")
		}, "connection refused"},
		{"cluster error", func(*http.Request) (*http.Response, error) {
			return esResponse(500, `{"error":{"type":"`+edgeSentinel+`"}}`), nil
		}, edgeSentinel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, logBuf := newTestServer(t, tc.rt)
			rec := get(t, s, "/healthz")
			if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"status\":\"error\"}\n" {
				t.Fatalf("healthz = %d %q, want 503 and exactly the frozen body", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), tc.cause) {
				t.Errorf("the cause reached the client: %q", rec.Body.String())
			}
			lg := queryLine(t, logBuf)
			cause, _ := lg["err"].(string)
			if !strings.Contains(cause, tc.cause) {
				t.Errorf("log line %v does not carry the cause %q", lg, tc.cause)
			}
			if lg["request_id"] == nil || lg["request_id"] != rec.Header().Get("X-Request-Id") {
				t.Errorf("log request_id %v, header %q", lg["request_id"], rec.Header().Get("X-Request-Id"))
			}
		})
	}
}
