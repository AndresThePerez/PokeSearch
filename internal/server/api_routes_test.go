package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serve runs one request with any method through the full middleware chain.
func serve(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// assertEnvelope checks the JSON error envelope an API client is promised on
// every non-2xx answer, including the request id echoed in the header.
func assertEnvelope(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := decodeError(t, rec)
	if body.Error.Code != code || body.Error.Message == "" || body.Error.Field != "" {
		t.Errorf("error = %+v, want code %q with a message and no field", body.Error, code)
	}
	if id := rec.Header().Get("X-Request-Id"); id == "" || body.RequestID != id {
		t.Errorf("request_id = %q, X-Request-Id = %q: must be present and equal", body.RequestID, id)
	}
}

func noESCalls(t *testing.T) roundTripperFunc {
	t.Helper()
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("routing answer must not call Elasticsearch: %s", r.URL.Path)
		return nil, nil
	})
}

// An API client that asks for a path the API does not have gets the same JSON
// envelope as every other API error, not the static file server's text page.
func TestUnknownAPIPathAnswersJSONNotFound(t *testing.T) {
	s, _ := newTestServer(t, noESCalls(t))
	for _, path := range []string{"/api/nope", "/api/v1/nope", "/api/search/extra", "/api/v1/sets/x/y", "/api/"} {
		t.Run(path, func(t *testing.T) {
			assertEnvelope(t, serve(t, s, http.MethodGet, path), http.StatusNotFound, codeNotFound)
		})
	}
}

// A wrong method on a route that exists keeps its 405 and the Allow header the
// mux computes, and gains the envelope with its own code.
func TestWrongMethodOnAPIPathAnswersJSONMethodNotAllowed(t *testing.T) {
	s, _ := newTestServer(t, noESCalls(t))
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/search"},
		{http.MethodDelete, "/api/v1/cards/base1-4"},
		{http.MethodPut, "/api/v1/sets"},
		{http.MethodOptions, "/api/suggest"},
		{http.MethodPost, "/api/nope"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := serve(t, s, tc.method, tc.path)
			assertEnvelope(t, rec, http.StatusMethodNotAllowed, codeMethodNotAllowed)
			if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
			}
		})
	}
}

// What the routing change must leave alone: HEAD on a GET route, the static
// site's own 404 and 405 for paths outside /api/, and the bare /api.
func TestRoutingOutsideTheEnvelopeUnchanged(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "_search") {
			return esResponse(200, searchESBody), nil
		}
		return esResponse(200, catalogESBody), nil
	})
	s, _ := newTestServer(t, rt)

	if rec := serve(t, s, http.MethodHead, "/api/search?q=pikachu"); rec.Code != http.StatusOK {
		t.Errorf("HEAD /api/search: status %d, want 200", rec.Code)
	}
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{http.MethodGet, "/nope", http.StatusNotFound, "404 page not found\n"},
		{http.MethodGet, "/api", http.StatusNotFound, "404 page not found\n"},
		{http.MethodGet, "/apix", http.StatusNotFound, "404 page not found\n"},
		{http.MethodPost, "/nope", http.StatusMethodNotAllowed, "Method Not Allowed\n"},
		{http.MethodPost, "/", http.StatusMethodNotAllowed, "Method Not Allowed\n"},
		{http.MethodPost, "/api", http.StatusMethodNotAllowed, "Method Not Allowed\n"},
	} {
		rec := serve(t, s, tc.method, tc.path)
		if rec.Code != tc.status || rec.Body.String() != tc.body {
			t.Errorf("%s %s: %d %q, want %d %q", tc.method, tc.path, rec.Code, rec.Body.String(), tc.status, tc.body)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s %s: Content-Type %q, want the plain-text page as before", tc.method, tc.path, ct)
		}
	}
	if got := serve(t, s, http.MethodGet, "/").Code; got != http.StatusOK {
		t.Errorf("GET /: status %d, want 200", got)
	}
}

// A request with a wrong API key on an unknown v1 path is still refused for
// the key first, exactly as before: the key check sits ahead of routing.
func TestUnknownV1PathWithWrongKeyIsStillUnauthorized(t *testing.T) {
	t.Setenv("POKESEARCH_API_KEYS", throwawayKey(t))
	s, _ := newTestServer(t, noESCalls(t))
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/nope", nil)
	req.Header.Set(apiKeyHeader, throwawayKey(t))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	assertEnvelope(t, rec, http.StatusUnauthorized, codeUnauthorized)
}
