package server

import (
	"net/http"
	"testing"
)

// Behind Cloudflare the ray id is set by the edge, while X-Request-Id is
// whatever the client chose to send, so the ray id names the request. Every
// combination of the two headers, through a route with an error body so the
// envelope's request_id is checked against the header as well.
func TestRequestIDPrefersCfRay(t *testing.T) {
	const ray, client = "8f0a1b2c3d4e5f60-IAD", "client-1"
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    string // "" means generated
	}{
		{name: "neither header", headers: nil},
		{name: "X-Request-Id only", headers: map[string]string{"X-Request-Id": client}, want: client},
		{name: "Cf-Ray only", headers: map[string]string{"Cf-Ray": ray}, want: ray},
		{name: "both headers", headers: map[string]string{"X-Request-Id": client, "Cf-Ray": ray}, want: ray},
		// A Cf-Ray that fails the same validation is no id at all, and the
		// client's id is used as before.
		{name: "invalid Cf-Ray, valid X-Request-Id", headers: map[string]string{"X-Request-Id": client, "Cf-Ray": "ray with spaces"}, want: client},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				t.Error("an invalid parameter must not reach Elasticsearch")
				return nil, nil
			}))
			rec := getWith(t, s, "/api/search?sort=bogus", tc.headers)
			got := rec.Header().Get("X-Request-Id")
			if body := decodeError(t, rec); body.RequestID != got {
				t.Errorf("request_id %q, X-Request-Id %q: must be equal", body.RequestID, got)
			}
			switch {
			case tc.want != "" && got != tc.want:
				t.Errorf("X-Request-Id = %q, want %q", got, tc.want)
			case tc.want == "" && (len(got) != 16 || got == client || got == ray):
				t.Errorf("X-Request-Id = %q, want a generated 16-hex id", got)
			}
		})
	}
}
