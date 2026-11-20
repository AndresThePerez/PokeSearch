package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// serveRaw runs a request whose target is taken verbatim, so doubled slashes,
// dot segments and percent-escapes reach the mux exactly as a client wrote them.
func serveRaw(t *testing.T, s *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(method, "http://example.test"+target, nil)
	if err != nil {
		t.Fatalf("%s %.40s: %v", method, target, err)
	}
	req.RequestURI = target
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func edgeRoutingServer(t *testing.T) *Server {
	t.Helper()
	// Every route that exists answers from this fake; the routing answers must
	// never reach it.
	s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return esResponse(503, `{"error":{"type":"unavailable"}}`), nil
	}))
	s.EnableMetrics()
	return s
}

// What a client saw on these requests before the envelope existed, and must
// still see: the status, the Allow header and the Location of a redirect. Only
// the body and Content-Type of an /api/ 404 or 405 may differ.
func TestRoutingAnswersKeepStatusAllowAndLocation(t *testing.T) {
	s := edgeRoutingServer(t)
	for _, tc := range []struct {
		method, target string
		status         int
		allow, loc     string
	}{
		// The mux cleans the path and redirects; the wrapper must not swallow it.
		{"GET", "/api//search", 307, "", "/api/search"},
		{"GET", "/api/./search", 307, "", "/api/search"},
		{"GET", "/api/../api/search", 307, "", "/api/search"},
		{"GET", "/api/v1/../search", 307, "", "/api/search"},
		{"HEAD", "/api//search", 307, "", "/api/search"},
		{"POST", "/api//search", 307, "", "/api/search"},
		{"DELETE", "/api/./search", 307, "", "/api/search"},
		// The bare /api is not an API path: it is the static server's, and it
		// is neither redirected to /api/ nor given the envelope.
		{"GET", "/api", 404, "", ""},
		{"HEAD", "/api", 404, "", ""},
		{"POST", "/api", 405, "GET, HEAD", ""},
		{"GET", "/apix", 404, "", ""},
		{"POST", "/apix", 405, "GET, HEAD", ""},
		{"GET", "/API/search", 404, "", ""},
		{"POST", "/API/search", 405, "GET, HEAD", ""},
		// Under /api/: 404 for GET and HEAD, 405 with the mux's Allow otherwise.
		{"GET", "/api/", 404, "", ""},
		{"HEAD", "/api/nope", 404, "", ""},
		{"GET", "/api/v1", 404, "", ""},
		{"GET", "/api/v1/", 404, "", ""},
		{"GET", "/api/v1/cards/a/b", 404, "", ""},
		{"POST", "/api/search", 405, "GET, HEAD", ""},
		{"PUT", "/api/v1/cards/x", 405, "GET, HEAD", ""},
		{"PATCH", "/api/v1/cards/", 405, "GET, HEAD", ""},
		{"TRACE", "/api/meta", 405, "GET, HEAD", ""},
		{"BREW", "/api/stats", 405, "GET, HEAD", ""},
		{"CONNECT", "/api/search", 405, "GET, HEAD", ""},
		{"OPTIONS", "/api/nope", 405, "GET, HEAD", ""},
		{"POST", "/api/", 405, "GET, HEAD", ""},
		// Other paths are untouched.
		{"GET", "/nope", 404, "", ""},
		{"POST", "/healthz", 405, "GET, HEAD", ""},
		{"POST", "/debug/vars", 405, "GET, HEAD", ""},
		{"GET", "/healthz/", 404, "", ""},
	} {
		rec := serveRaw(t, s, tc.method, tc.target)
		if rec.Code != tc.status || rec.Header().Get("Allow") != tc.allow || rec.Header().Get("Location") != tc.loc {
			t.Errorf("%s %s: %d allow=%q location=%q, want %d allow=%q location=%q",
				tc.method, tc.target, rec.Code, rec.Header().Get("Allow"), rec.Header().Get("Location"),
				tc.status, tc.allow, tc.loc)
		}
	}
}

// Only a routing failure under /api/ is re-answered. Everything outside keeps
// the static server's plain-text page, whatever the method.
func TestRoutingErrorsOutsideAPIStayPlainText(t *testing.T) {
	s := edgeRoutingServer(t)
	for _, method := range []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "TRACE", "BREW"} {
		for _, target := range []string{"/nope", "/apix", "/api", "/API/search", "/ap", "/index.htm", "/" + strings.Repeat("a", 8000)} {
			rec := serveRaw(t, s, method, target)
			if rec.Code != 404 && rec.Code != 405 {
				continue
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
				t.Errorf("%s %.30s: Content-Type %q on a %d, want the plain-text page", method, target, ct, rec.Code)
			}
		}
	}
}

type envelopeBody struct {
	Error     map[string]any `json:"error"`
	RequestID string         `json:"request_id"`
}

// The envelope is exactly {error:{code,message}, request_id} and nothing the
// request supplied is written into it, however hostile the path.
func TestAPIErrorEnvelopeEchoesNothingFromTheRequest(t *testing.T) {
	s := edgeRoutingServer(t)
	for _, target := range []string{
		"/api/%22%3Cscript%3Ealert(1)%3C%2Fscript%3E",
		"/api/line%0aSet-Cookie:%20x=1",
		"/api/%e2%80%ae%e2%80%8b",
		"/api/" + strings.Repeat("A", 8000),
		"/api/%00",
		"/api/v1/cards/a/b%0d%0a",
	} {
		for _, method := range []string{"GET", "POST", "HEAD"} {
			rec := serveRaw(t, s, method, target)
			if rec.Code != 404 && rec.Code != 405 {
				t.Errorf("%s %.40s: status %d, want 404 or 405", method, target, rec.Code)
				continue
			}
			if method == "HEAD" {
				// The recorder, unlike net/http's server, does not drop the body
				// of a HEAD; the status and headers are what is checked here.
				if rec.Header().Get("Content-Type") != "application/json" {
					t.Errorf("HEAD %.40s: Content-Type %q", target, rec.Header().Get("Content-Type"))
				}
				continue
			}
			var env envelopeBody
			dec := json.NewDecoder(strings.NewReader(rec.Body.String()))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&env); err != nil {
				t.Errorf("%s %.40s: body is not the envelope: %v: %.100s", method, target, err, rec.Body.String())
				continue
			}
			if len(env.Error) != 2 || env.Error["code"] == nil || env.Error["message"] == nil {
				t.Errorf("%s %.40s: error object %v, want exactly code and message", method, target, env.Error)
			}
			if env.RequestID == "" || env.RequestID != rec.Header().Get("X-Request-Id") {
				t.Errorf("%s %.40s: request_id %q, header %q", method, target, env.RequestID, rec.Header().Get("X-Request-Id"))
			}
			decoded, _ := url.PathUnescape(strings.TrimPrefix(target, "/api/"))
			for _, frag := range []string{decoded, strings.TrimPrefix(target, "/api/"), "script", "Set-Cookie", "AAAA"} {
				if frag != "" && strings.Contains(rec.Body.String(), frag) {
					t.Errorf("%s %.40s: body echoes %.20q: %.100s", method, target, frag, rec.Body.String())
				}
			}
		}
	}
}

// The mux's own error handler runs against a throwaway writer to learn the
// status and Allow. None of what it wrote there may reach the real response:
// the headers of the 404 and the 405 are the ones writeError and the security
// middleware set, plus Allow on the 405.
func TestAPIRoutingProbeLeaksNoHeadersIntoTheResponse(t *testing.T) {
	s := edgeRoutingServer(t)
	reference := serveRaw(t, s, "GET", "/api/search?sort=bogus") // an envelope written by writeError alone
	if reference.Code != 400 {
		t.Fatalf("reference status %d, want 400", reference.Code)
	}
	want := map[string]bool{}
	for k := range reference.Header() {
		want[k] = true
	}
	for _, tc := range []struct {
		method, target string
		extra          []string
	}{
		{"GET", "/api/nope", nil},
		{"HEAD", "/api/nope", nil},
		{"POST", "/api/search", []string{"Allow"}},
		{"DELETE", "/api/v1/cards/x", []string{"Allow"}},
	} {
		rec := serveRaw(t, s, tc.method, tc.target)
		got := map[string]bool{}
		for k := range rec.Header() {
			got[k] = true
		}
		for _, k := range tc.extra {
			if !got[k] {
				t.Errorf("%s %s: missing %s", tc.method, tc.target, k)
			}
			delete(got, k)
		}
		for k := range got {
			if !want[k] {
				t.Errorf("%s %s: header %s=%q appears on a routing answer but not on an ordinary error", tc.method, tc.target, k, rec.Header().Get(k))
			}
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s %s: Content-Type %q, want application/json", tc.method, tc.target, ct)
		}
		if len(rec.Header().Values("Content-Type")) != 1 || len(rec.Header().Values("X-Content-Type-Options")) != 1 {
			t.Errorf("%s %s: duplicated header values: %v", tc.method, tc.target, rec.Header())
		}
	}
}

// The Allow header is the mux's, byte for byte, for every method that has no
// pattern on a path that has one.
func TestAPIAllowHeaderMatchesTheMuxByteForByte(t *testing.T) {
	s := edgeRoutingServer(t)
	for _, target := range []string{"/api/search", "/api/suggest", "/api/explain", "/api/compare", "/api/stats", "/api/meta", "/api/v1/cards", "/api/v1/cards/x", "/api/v1/sets"} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS", "TRACE", "BREW"} {
			direct := httptest.NewRecorder()
			req := httptest.NewRequest(method, target, nil)
			s.mux.ServeHTTP(direct, req)
			if direct.Code != 405 {
				t.Fatalf("%s %s: the mux answered %d, test premise broken", method, target, direct.Code)
			}
			got := serve(t, s, method, target)
			if got.Code != 405 || got.Header().Get("Allow") != direct.Header().Get("Allow") {
				t.Errorf("%s %s: %d Allow=%q, want 405 Allow=%q", method, target, got.Code, got.Header().Get("Allow"), direct.Header().Get("Allow"))
			}
		}
	}
}

// The key check sits ahead of routing, so a missing route or method is only
// reported to a caller the key check let through.
func TestV1RoutingErrorsFollowTheKeyCheck(t *testing.T) {
	good, wrong := throwawayKey(t), throwawayKey(t)
	t.Setenv("POKESEARCH_API_KEYS", good)
	s, _ := newTestServer(t, noESCalls(t))
	const challenge = `ApiKey realm="pokesearch", header="X-Api-Key"`
	for _, tc := range []struct {
		method, target, key string
		status              int
		code                string
		challenge           bool
	}{
		{"GET", "/api/v1/nope", wrong, 401, codeUnauthorized, true},
		{"DELETE", "/api/v1/cards/x", wrong, 401, codeUnauthorized, true},
		{"GET", "/api/v1/nope", good, 404, codeNotFound, false},
		{"DELETE", "/api/v1/cards/x", good, 405, codeMethodNotAllowed, false},
		{"POST", "/api/v1/sets", good, 405, codeMethodNotAllowed, false},
		// No key at all is the public demo: the route decides.
		{"GET", "/api/v1/nope", "", 404, codeNotFound, false},
		{"DELETE", "/api/v1/cards/x", "", 405, codeMethodNotAllowed, false},
		// A wrong key outside /api/v1/ is not looked at.
		{"GET", "/api/nope", wrong, 404, codeNotFound, false},
		{"POST", "/api/search", wrong, 405, codeMethodNotAllowed, false},
	} {
		req := httptest.NewRequest(tc.method, tc.target, nil)
		if tc.key != "" {
			req.Header.Set(apiKeyHeader, tc.key)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		assertEnvelope(t, rec, tc.status, tc.code)
		if got := rec.Header().Get("WWW-Authenticate"); (got == challenge) != tc.challenge || (got != "" && !tc.challenge) {
			t.Errorf("%s %s key=%v: WWW-Authenticate %q, want challenge=%v", tc.method, tc.target, tc.key != "", got, tc.challenge)
		}
	}
}

// Whatever method and path a client sends, the router answers: no panic, no
// server error, an envelope exactly when the path is under /api/ and the
// answer is a 404 or 405.
func FuzzAPIRouting(f *testing.F) {
	for _, m := range []string{"GET", "POST", "HEAD", "CONNECT", "BREW"} {
		for _, p := range []string{"/api/nope", "/api/search", "/api", "/api//x", "/api/%2e%2e/healthz", "/api%2Fsearch", "//api/x"} {
			f.Add(m, p)
		}
	}
	f.Fuzz(func(t *testing.T, method, target string) {
		s := edgeRoutingServer(t)
		if !strings.HasPrefix(target, "/") || strings.ContainsAny(method, " \t\r\n") || method == "" {
			t.Skip()
		}
		req, err := http.NewRequest(method, "http://example.test"+target, nil)
		if err != nil {
			t.Skip()
		}
		req.RequestURI = target
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		// A route that exists may answer 503 from the fake cluster; a path no
		// route owns may not answer with a server error at all.
		if _, pattern := s.mux.Handler(req); rec.Code >= 500 && (pattern == "" || pattern == "GET /" || pattern == "GET /api" || pattern == "GET /api/") {
			t.Fatalf("%s %q: status %d from pattern %q", method, target, rec.Code, pattern)
		}
		// Judged on the escaped path: the mux routes on it, so a %2F inside the
		// first segment is not a path under /api/ as far as routing goes.
		if strings.HasPrefix(req.URL.EscapedPath(), "/api/") && (rec.Code == 404 || rec.Code == 405) {
			if rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("%s %q: %d with Content-Type %q", method, target, rec.Code, rec.Header().Get("Content-Type"))
			}
			if rec.Code == 405 && rec.Header().Get("Allow") == "" {
				t.Fatalf("%s %q: 405 without Allow", method, target)
			}
		}
	})
}
