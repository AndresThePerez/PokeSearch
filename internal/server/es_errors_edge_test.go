package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// es404Bodies are the replies a 404 can carry: the two shapes Elasticsearch
// sends for a document that is not there, its missing-index report, and the
// malformed or ambiguous bodies a proxy or a cut log snippet can produce.
// errorReport marks the bodies that report a failure through an "error"
// member rather than answer for a missing document.
var es404Bodies = []struct {
	name        string
	body        string
	errorReport bool
}{
	{"explain of a missing document", `{"_index":"cards","_id":"x","matched":false}`, false},
	{"get of a missing document", `{"_index":"cards","_id":"x","found":false}`, false},
	{"missing index", `{"error":{"root_cause":[{"type":"index_not_found_exception","reason":"no such index [cards]",` +
		`"index":"cards"}],"type":"index_not_found_exception","reason":"no such index [cards]","index":"cards"},"status":404}`, true},
	{"empty body", ``, false},
	{"html page from a proxy", `<html><body><h1>404 Not Found</h1></body></html>`, false},
	{"error null", `{"error":null,"status":404}`, false},
	{"error empty string", `{"error":""}`, true},
	{"error string", `{"error":"no such index"}`, true},
	{"error empty object", `{"error":{}}`, true},
	{"error past the log cap", `{"_index":"cards","pad":"` + strings.Repeat("p", 3*esErrorBodyLimit) +
		`","error":{"type":"index_not_found_exception"}}`, false},
	{"exception name before the log cap", `{"error":{"type":"index_not_found_exception","pad":"` +
		strings.Repeat("p", 3*esErrorBodyLimit) + `"}}`, true},
	{"found false with an error member", `{"_index":"cards","_id":"x","found":false,"error":{"type":"x_exception"}}`, true},
}

// Every route sees the same 404 classification, and only explain reads it:
// a 404 that answers for a missing document is still found:false, a 404 that
// reports an error is the outage 503, and every other route answers exactly
// what it answered before that distinction existed (healthz and meta read a
// 404 as the not-seeded-yet signal; the rest fail with the 503 contract).
func TestES404ClassificationOnEveryRoute(t *testing.T) {
	routes := []struct {
		path string
		// want is the status for a body; nil means the route does not read
		// the distinction and wantFixed holds.
		want      func(errorReport bool) int
		wantFixed int
	}{
		{"/api/explain?id=base1-4&q=charizard", func(errorReport bool) int {
			if errorReport {
				return http.StatusServiceUnavailable
			}
			return http.StatusOK
		}, 0},
		{"/api/search?q=charizard", nil, http.StatusServiceUnavailable},
		{"/api/search", nil, http.StatusServiceUnavailable},
		{"/api/suggest?q=pika", nil, http.StatusServiceUnavailable},
		{"/api/compare?q=charizard", nil, http.StatusServiceUnavailable},
		{"/api/stats", nil, http.StatusServiceUnavailable},
		{"/api/meta", nil, http.StatusOK},
		{"/healthz", nil, http.StatusOK},
		{"/api/v1/cards/base1-4", nil, http.StatusServiceUnavailable},
		{"/api/v1/cards?ids=base1-4,base1-2", nil, http.StatusServiceUnavailable},
		{"/api/v1/sets", nil, http.StatusServiceUnavailable},
	}
	for _, route := range routes {
		for _, b := range es404Bodies {
			t.Run(route.path+"/"+b.name, func(t *testing.T) {
				s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
					return esResponse(http.StatusNotFound, b.body), nil
				}))
				rec := get(t, s, route.path)
				want := route.wantFixed
				if route.want != nil {
					want = route.want(b.errorReport)
				}
				if rec.Code != want {
					t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body.String())
				}
				switch {
				case route.path == "/healthz":
					if rec.Body.String() != "{\"docs\":0,\"status\":\"ok\"}\n" {
						t.Errorf("healthz body %q, want the not-seeded-yet signal", rec.Body.String())
					}
				case want == http.StatusServiceUnavailable:
					if body := decodeError(t, rec); body.Error.Code != codeESUnavailable {
						t.Errorf("error %+v, want es_unavailable", body.Error)
					}
				case strings.HasPrefix(route.path, "/api/explain"):
					if !strings.Contains(rec.Body.String(), `"found":false`) {
						t.Errorf("a missing document must be found:false: %s", rec.Body.String())
					}
				}
			})
		}
	}
}

// A v1 card that is not in the index is still the 404 not_found answer: the
// _mget a v1 lookup makes reports a missing document inside a 200, which the
// new 404 reading never sees.
func TestV1MissingCardStaysNotFound(t *testing.T) {
	s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return esResponse(http.StatusOK, `{"docs":[{"_index":"cards","_id":"zz9-1","found":false}]}`), nil
	}))
	rec := get(t, s, "/api/v1/cards/zz9-1")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); body.Error.Code != codeNotFound || body.Error.Field != "id" {
		t.Errorf("error %+v, want not_found on id", body.Error)
	}
}

// Only the request built from the client's own input can be the client's
// fault. A search whose main query succeeds but whose set-catalog read is
// rejected is the cluster failing (503); a rejected did-you-mean is a dropped
// hint on a successful search (200, no correction).
func TestES400OnASecondarySearchCallIsNotTheClientsError(t *testing.T) {
	const okSearch = `{"took":3,"hits":{"total":{"value":0},"hits":[]},"aggregations":{}}`
	t.Run("set catalog", func(t *testing.T) {
		var calls atomic.Int32
		s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return esResponse(http.StatusOK, okSearch), nil
			}
			return esResponse(http.StatusBadRequest, `{"error":{"type":"parsing_exception"},"status":400}`), nil
		}))
		rec := get(t, s, "/api/search")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status %d after %d calls, want 503: %s", rec.Code, calls.Load(), rec.Body.String())
		}
		if body := decodeError(t, rec); body.Error.Code != codeESUnavailable {
			t.Errorf("error %+v, want es_unavailable", body.Error)
		}
	})
	t.Run("did you mean", func(t *testing.T) {
		var calls atomic.Int32
		s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			body, _ := io.ReadAll(r.Body)
			if bytes.Contains(body, []byte(`"suggest"`)) {
				return esResponse(http.StatusBadRequest, `{"error":{"type":"parsing_exception"},"status":400}`), nil
			}
			if bytes.Contains(body, []byte(`set_catalog`)) {
				return esResponse(http.StatusOK, `{"took":1,"hits":{"total":{"value":0},"hits":[]},"aggregations":{"set_catalog":{"buckets":[]}}}`), nil
			}
			return esResponse(http.StatusOK, okSearch), nil
		}))
		rec := get(t, s, "/api/search?q=pikchu")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d after %d calls, want 200: %s", rec.Code, calls.Load(), rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"did_you_mean":"`) {
			t.Errorf("a rejected suggester must not produce a correction: %s", rec.Body.String())
		}
	})
}

// countingReader reports how many bytes the server pulled from an ES body.
type countingReader struct {
	r    io.Reader
	read atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read.Add(int64(n))
	return n, err
}

// However a rejection reads, the client gets exactly the standard envelope
// (code, message, request_id, no field) and nothing of the cluster's reply,
// and a huge reply is read only up to the log cap.
func TestES400ClientBodyIsTheBareEnvelope(t *testing.T) {
	hostile := `{"error":{"type":"x_exception","reason":"` + edgeSentinel +
		` \"quoted\" <script>alert(1)</script>\nline two"},"status":400}`
	bodies := map[string]func() io.Reader{
		"empty":    func() io.Reader { return strings.NewReader("") },
		"not json": func() io.Reader { return strings.NewReader("<html>" + edgeSentinel + "</html>") },
		"hostile":  func() io.Reader { return strings.NewReader(hostile) },
		"ten megabytes": func() io.Reader {
			return io.MultiReader(strings.NewReader(`{"error":"`+edgeSentinel), bytes.NewReader(bytes.Repeat([]byte("x"), 10<<20)))
		},
	}
	for _, path := range []string{
		"/api/search?q=charizard", "/api/suggest?q=pika",
		"/api/explain?id=base1-4&q=charizard", "/api/compare?q=charizard",
	} {
		for name, body := range bodies {
			t.Run(path+"/"+name, func(t *testing.T) {
				var readers []*countingReader
				s, logBuf := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
					cr := &countingReader{r: body()}
					readers = append(readers, cr)
					res := esResponse(http.StatusBadRequest, "")
					res.Body = io.NopCloser(cr)
					return res, nil
				}))
				rec := get(t, s, path)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
					t.Fatalf("undecodable body %q: %v", rec.Body.String(), err)
				}
				if len(raw) != 2 || raw["error"] == nil || raw["request_id"] == nil {
					t.Errorf("top-level keys of %s, want exactly error and request_id", rec.Body.String())
				}
				var inner map[string]string
				if err := json.Unmarshal(raw["error"], &inner); err != nil {
					t.Fatal(err)
				}
				if _, hasField := inner["field"]; hasField || len(inner) != 2 || inner["code"] != codeInvalidParam || inner["message"] == "" {
					t.Errorf("error object %v, want only code invalid_param and a message", inner)
				}
				for _, leak := range []string{edgeSentinel, "quoted", "script", "line two", "html", "x_exception"} {
					if strings.Contains(rec.Body.String(), leak) {
						t.Errorf("client body carries %q: %s", leak, rec.Body.String())
					}
				}
				for i, cr := range readers {
					if got := cr.read.Load(); got > 2*esErrorBodyLimit {
						t.Errorf("reply %d: read %d bytes of a rejection, want at most the log cap", i, got)
					}
				}
				if logBuf.Len() > 4*(esErrorBodyLimit+4096) {
					t.Errorf("log grew to %d bytes for one rejected request", logBuf.Len())
				}
			})
		}
	}
}

// healthz writes its cause line only when it fails: a healthy probe and the
// not-seeded-yet 404 are not failures and stay silent.
func TestHealthzLogsNothingWhenItAnswersOK(t *testing.T) {
	for name, reply := range map[string]func() *http.Response{
		"healthy":       func() *http.Response { return esResponse(http.StatusOK, `{"count":20635}`) },
		"missing index": func() *http.Response { return esResponse(http.StatusNotFound, es404Bodies[2].body) },
	} {
		t.Run(name, func(t *testing.T) {
			s, logBuf := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return reply(), nil
			}))
			rec := get(t, s, "/healthz")
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			for _, m := range logLines(t, logBuf) {
				if m["msg"] != "access" {
					t.Errorf("a passing healthz logged %v", m)
				}
			}
		})
	}
}

// fakePassword is an obviously made-up credential: it must never reach a log.
const fakePassword = "fake-password-0000"

// The new healthz line carries the transport error, and transport errors
// commonly quote the request URL. The URL the client sends must not carry the
// address's userinfo, so the password never reaches the line.
func TestHealthzCauseLineNeverCarriesTheESPassword(t *testing.T) {
	var logBuf bytes.Buffer
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: errors.New("connect: connection refused")}
	})
	s := newTestServerAt(t, "http://elastic:"+fakePassword+"@fake-es:9200", rt, &logBuf)
	rec := get(t, s, "/healthz")
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"status\":\"error\"}\n" {
		t.Fatalf("healthz = %d %q, want 503 and the frozen body", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logBuf.String(), "connection refused") {
		t.Errorf("the cause is missing from the log: %s", logBuf.String())
	}
	if strings.Contains(logBuf.String(), fakePassword) {
		t.Errorf("the log carries the Elasticsearch password: %s", logBuf.String())
	}
}
