package server

import (
	"net/http"
	"testing"
)

// A 401 names how to authenticate (RFC 9110 §11.6.1). The v1 routes take a key
// in the X-Api-Key header and in no other way, so that is the one challenge.
func TestV1UnauthorizedNamesTheKeyHeader(t *testing.T) {
	good, wrong := throwawayKey(t), throwawayKey(t)
	t.Setenv("POKESEARCH_API_KEYS", good)
	s, _ := newTestServer(t, newFakeCards(t, v1Card("base1-4", "Charizard")))

	for _, path := range []string{"/api/v1/sets", "/api/v1/cards/base1-4", "/api/v1/cards?ids=base1-4"} {
		rec := getKeyed(t, s, path, wrong)
		body := assertV1Error(t, rec, http.StatusUnauthorized, codeUnauthorized, "")
		if body.Error.Message != "invalid API key" {
			t.Errorf("%s: message = %q, want the unchanged %q", path, body.Error.Message, "invalid API key")
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != `ApiKey realm="pokesearch", header="X-Api-Key"` {
			t.Errorf("%s: WWW-Authenticate = %q, want the X-Api-Key challenge", path, got)
		}
	}

	// Only the refusal carries the challenge.
	for _, rec := range []*http.Response{
		getKeyed(t, s, "/api/v1/cards/base1-4", good).Result(),
		get(t, s, "/api/v1/cards/base1-4").Result(),
	} {
		if got := rec.Header.Get("WWW-Authenticate"); got != "" {
			t.Errorf("status %d carries WWW-Authenticate %q", rec.StatusCode, got)
		}
	}
}
